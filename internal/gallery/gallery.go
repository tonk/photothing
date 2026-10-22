// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

// Package gallery scans the photo directory and produces resized images.
package gallery

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/semaphore"

	"photothing/internal/store"
)

// metaVersion numbers what is read from a file into the database (dimensions, EXIF fields, ...). When
// a release reads more, it raises this number and the next scan reads every file again, once, so
// existing photos get the new details without anybody touching their files.
const metaVersion = 4

const metaVersionKey = "meta_version"

const (
	maxPixels = 100_000_000 // refuse decompression bombs
	// A decoded 100-megapixel image takes about half a gigabyte (measured: 533 MB for one thumbnail). This is the
	// number of pixels that may be in memory at once for all requests together, about 850 MB: one huge image
	// and a few small ones, or many small ones. Further requests wait their turn.
	maxPixelsInFlight = 160_000_000
	// Resizing is CPU work; more than this many at once only makes each of them slower.
	maxConcurrentResizes = 4
	ThumbSize            = 360
	LargeSize            = 1920
)

type Library struct {
	RootPath  string // absolute, symlinks resolved
	root      *os.Root
	CacheDir  string
	Tools     Tools
	store     *store.Store
	scanMu    sync.Mutex
	resizeSem chan struct{}
	copySem   chan struct{} // limits the video copies being made
	budget    *semaphore.Weighted
	budgetMax int64 // the capacity of budget
	// decodeHook, when set (tests), is called with +pixels when a decode starts and -pixels when it ends.
	decodeHook func(delta int64)
}

func Open(rootPath, cacheDir string, st *store.Store) (*Library, error) {
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	return &Library{RootPath: abs, root: root, CacheDir: cacheDir, Tools: DetectTools(), store: st,
		resizeSem: make(chan struct{}, min(runtime.NumCPU(), maxConcurrentResizes)), copySem: make(chan struct{}, 2), budget: semaphore.NewWeighted(maxPixelsInFlight), budgetMax: maxPixelsInFlight}, nil
}

// OpenOriginal opens a photo by its root-relative path; it cannot escape the root.
func (l *Library) OpenOriginal(rel string) (*os.File, error) {
	return l.root.Open(filepath.FromSlash(rel))
}

// abs returns the absolute path of a photo for the external tools, which cannot work through os.Root.
// Only regular files qualify: a symbolic link put in the photo directory after the scan must not make a
// tool read some other file. (The check and the use are not atomic; the photo directory is meant to be
// written by trusted people only.)
func (l *Library) abs(rel string) (string, error) {
	fi, err := l.root.Lstat(filepath.FromSlash(rel))
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", rel)
	}
	return filepath.Join(l.RootPath, filepath.FromSlash(rel)), nil
}

type ScanResult struct {
	Added, Updated, Moved, Removed, Skipped int
	MissingTools                            map[string]int // tool group -> number of files skipped for lack of it
}

func (r ScanResult) String() string {
	s := fmt.Sprintf("%d added, %d updated, %d moved, %d removed, %d skipped", r.Added, r.Updated, r.Moved, r.Removed, r.Skipped)
	for tool, n := range r.MissingTools {
		s += fmt.Sprintf("; %d file(s) need the %q tools", n, tool)
	}
	return s
}

type pending struct {
	rel   string
	info  fs.FileInfo
	known bool
}

type fileKey struct{ size, mtime int64 }

// Scan synchronises the database with the photo directory. Hidden entries and symlinks are ignored.
// A file that disappeared while exactly one new file with the same size and modification time
// appeared is treated as a rename: it keeps its id, caption, tags and album covers.
func (l *Library) Scan(ctx context.Context) (ScanResult, error) {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()

	res := ScanResult{MissingTools: map[string]int{}}
	reread := l.store.Setting(metaVersionKey) != strconv.Itoa(metaVersion)
	index, err := l.store.PhotoIndex()
	if err != nil {
		return res, err
	}
	seen := map[string]bool{}
	var todo []pending

	err = filepath.WalkDir(l.RootPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if p == l.RootPath {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if _, ok := typeOf(d.Name()); !ok {
			return nil
		}
		rel, err := filepath.Rel(l.RootPath, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		seen[rel] = true
		old, known := index[rel]
		if known && !reread && old.MTime == info.ModTime().Unix() && old.Size == info.Size() {
			return nil
		}
		todo = append(todo, pending{rel, info, known})
		return nil
	})
	if err != nil {
		return res, err
	}

	// Detect renames: unambiguous (size, mtime) pairs between vanished and new files.
	gone := map[fileKey][]string{}
	for p, e := range index {
		if !seen[p] {
			k := fileKey{e.Size, e.MTime}
			gone[k] = append(gone[k], p)
		}
	}
	fresh := map[fileKey]int{}
	for _, t := range todo {
		if !t.known {
			fresh[fileKey{t.info.Size(), t.info.ModTime().Unix()}]++
		}
	}
	moved := map[string]bool{} // old paths that were renamed

	for _, t := range todo {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		k := fileKey{t.info.Size(), t.info.ModTime().Unix()}
		if !t.known && fresh[k] == 1 && len(gone[k]) == 1 {
			oldPath := gone[k][0]
			dir := path.Dir(t.rel)
			if dir == "." {
				dir = ""
			}
			if err := l.store.MovePhoto(index[oldPath].ID, t.rel, dir, path.Base(t.rel)); err != nil {
				return res, err
			}
			moved[oldPath] = true
			res.Moved++
			continue
		}
		ph, err := l.readPhoto(ctx, t.rel, t.info)
		if err != nil {
			res.Skipped++
			if err == errMissingTool {
				ft, _ := typeOf(t.rel)
				res.MissingTools[ft.Tool]++
			} else {
				fmt.Fprintf(os.Stderr, "scan: skip %s: %v\n", t.rel, err)
			}
			continue
		}
		if err := l.store.UpsertPhoto(ph); err != nil {
			return res, err
		}
		if t.known {
			res.Updated++
		} else {
			res.Added++
		}
	}
	for p, e := range index {
		if !seen[p] && !moved[p] {
			if err := l.store.DeletePhoto(e.ID); err != nil {
				return res, err
			}
			res.Removed++
		}
	}
	if reread { // only after a complete pass, so an interrupted scan starts over
		if err := l.store.SetSetting(metaVersionKey, strconv.Itoa(metaVersion)); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (l *Library) readPhoto(ctx context.Context, rel string, info fs.FileInfo) (*store.Photo, error) {
	ft, _ := typeOf(rel)
	ph := &store.Photo{Path: rel, Filename: path.Base(rel), Size: info.Size(), MTime: info.ModTime().Unix(), Kind: ft.Kind}
	if d := path.Dir(rel); d != "." {
		ph.Dir = d
	}
	var meta exifMeta
	switch {
	case ft.Native:
		f, err := l.root.Open(filepath.FromSlash(rel))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		cfg, _, err := image.DecodeConfig(f)
		if err != nil {
			return nil, err
		}
		ph.Width, ph.Height = cfg.Width, cfg.Height
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		meta = readExif(f)
		if staleOrientation(meta.Orientation, cfg.Width, cfg.Height, meta.PixelX, meta.PixelY) {
			meta.Display[LabelOrientNote] = orientationIgnored // the dimensions are already those of the upright photo
		} else if meta.Orientation >= 5 && meta.Orientation <= 8 {
			ph.Width, ph.Height = ph.Height, ph.Width
		}
	case ft.Kind == "video":
		if !l.Tools.Has(ft.Tool) {
			return nil, errMissingTool
		}
		abs, err := l.abs(rel)
		if err != nil {
			return nil, err
		}
		vi, err := l.probeVideo(ctx, abs, ft.Demux)
		if err != nil {
			return nil, err
		}
		ph.Width, ph.Height = vi.W, vi.H
		meta = exifMeta{Display: map[string]string{LabelDuration: formatDuration(vi.Duration)}, Taken: vi.Created}
		if vi.Codec != "" {
			meta.Display[LabelCodec] = vi.Codec
		}
	default: // HEIC and RAW: dimensions of the rendered JPEG, metadata from exiftool
		abs, err := l.abs(rel)
		if err != nil {
			return nil, err
		}
		jpg, cleanup, err := l.toJPEG(ctx, abs, ft)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		f, err := os.Open(jpg)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		cfg, _, err := image.DecodeConfig(f)
		if err != nil {
			return nil, err
		}
		ph.Width, ph.Height = cfg.Width, cfg.Height
		meta = l.exiftoolMeta(ctx, abs)
	}
	if ph.Width*ph.Height > maxPixels {
		return nil, fmt.Errorf("image too large (%dx%d)", ph.Width, ph.Height)
	}
	if meta.Display == nil {
		meta.Display = map[string]string{}
	}
	ph.Exif = meta.Display
	ph.TakenAt = meta.Taken
	if ph.TakenAt.IsZero() {
		ph.TakenAt = info.ModTime()
	}
	return ph, nil
}

// orientationIgnoredFor reports whether the Orientation tag of this photo was found to be stale at scan time.
func orientationIgnoredFor(p *store.Photo) bool {
	_, ignored := p.Exif[LabelOrientNote]
	return ignored
}

// decode returns the image of a photo (for videos: a frame), with EXIF orientation applied.
func (l *Library) decode(ctx context.Context, p *store.Photo) (image.Image, error) {
	ft, _ := typeOf(p.Filename)
	if ft.Native {
		f, err := l.OpenOriginal(p.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return imaging.Decode(f, imaging.AutoOrientation(!orientationIgnoredFor(p)))
	}
	abs, err := l.abs(p.Path)
	if err != nil {
		return nil, err
	}
	jpg, cleanup, err := l.toJPEG(ctx, abs, ft)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	// libheif and ffmpeg turn the picture themselves, so whatever orientation tag is left in their output
	// must not be applied again; the preview of a RAW file may still carry one.
	return imaging.Open(jpg, imaging.AutoOrientation(ft.Tool == "raw"))
}

// Variant returns the path of a cached resized JPEG, generating it when needed.
// kind is "thumb" (square crop) or "large" (fit within LargeSize).
func (l *Library) Variant(ctx context.Context, p *store.Photo, kind string) (string, error) {
	if kind != "thumb" && kind != "large" {
		return "", fmt.Errorf("unknown variant %q", kind)
	}
	out := filepath.Join(l.CacheDir, fmt.Sprintf("%d_%s_%d.jpg", p.ID, kind, p.MTime))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	select {
	case l.resizeSem <- struct{}{}:
		defer func() { <-l.resizeSem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// Wait for memory too. An image bigger than the whole budget gets all of it, and so runs alone.
	need := min(int64(p.Width)*int64(p.Height), l.budgetMax)
	if need < 1 {
		need = 1
	}
	if err := l.budget.Acquire(ctx, need); err != nil {
		return "", err
	}
	defer l.budget.Release(need)
	if l.decodeHook != nil {
		l.decodeHook(need)
		defer l.decodeHook(-need)
	}

	img, err := l.decode(ctx, p)
	if err != nil {
		return "", err
	}
	quality := 88
	if kind == "thumb" {
		img = imaging.Fill(img, ThumbSize, ThumbSize, imaging.Center, imaging.Lanczos)
		quality = 82
	} else if b := img.Bounds(); b.Dx() > LargeSize || b.Dy() > LargeSize {
		img = imaging.Fit(img, LargeSize, LargeSize, imaging.Lanczos)
	}
	tmp, err := os.CreateTemp(l.CacheDir, "tmp-*.jpg")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := jpeg.Encode(tmp, img, &jpeg.Options{Quality: quality}); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return "", err
	}
	return out, nil
}

// setBudget replaces the pixel budget (tests use small ones).
func (l *Library) setBudget(pixels int64) {
	l.budget, l.budgetMax = semaphore.NewWeighted(pixels), pixels
}

// WebVideo returns the copy of a video that is shown to users who may not download originals: the same
// audio and video streams, copied without re-encoding, with all metadata dropped. Camera videos carry
// the location they were filmed at (and more) in that metadata, and "may not download the original"
// would mean little if the player's source were the original. The copy is made once and cached.
func (l *Library) WebVideo(ctx context.Context, p *store.Photo) (string, error) {
	ft, ok := typeOf(p.Filename)
	if !ok || ft.Kind != "video" {
		return "", fmt.Errorf("%s is not a video", p.Filename)
	}
	out := filepath.Join(l.CacheDir, fmt.Sprintf("%d_vcopy_%d%s", p.ID, p.MTime, strings.ToLower(filepath.Ext(p.Filename))))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if !l.Tools.Has(ft.Tool) {
		return "", errMissingTool
	}
	abs, err := l.abs(p.Path)
	if err != nil {
		return "", err
	}
	select {
	case l.copySem <- struct{}{}:
		defer func() { <-l.copySem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	tmp, err := os.CreateTemp(l.CacheDir, "tmp-*"+strings.ToLower(filepath.Ext(p.Filename)))
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	args := append([]string{"-nostdin", "-loglevel", "error"}, ffmpegInput(ft.Demux)...)
	args = append(args, "-i", abs, "-map", "0:v:0", "-map", "0:a?", "-sn", "-dn", "-c", "copy",
		"-map_metadata", "-1", "-map_chapters", "-1")
	if ft.Mux == "mp4" || ft.Mux == "mov" {
		args = append(args, "-movflags", "+faststart") // lets the browser start playing before the whole file is there
	}
	args = append(args, "-f", ft.Mux, "-y", tmp.Name())
	if _, err := run(ctx, 10*time.Minute, l.Tools.FFmpeg, args...); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return "", err
	}
	return out, nil
}

// cacheName matches the files of the image cache: <id>_<kind>_<mtime>.<ext>.
var cacheName = regexp.MustCompile(`^(\d+)_(thumb|large|vcopy)_(\d+)\.[a-z0-9]+$`)

// PruneCache removes cached variants whose photo no longer exists or whose mtime is stale,
// and leftovers of interrupted conversions.
func (l *Library) PruneCache() {
	entries, _ := os.ReadDir(l.CacheDir)
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		full := filepath.Join(l.CacheDir, e.Name())
		if strings.HasPrefix(e.Name(), "tmp-") {
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				os.RemoveAll(full)
			}
			continue
		}
		m := cacheName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		id, _ := strconv.ParseInt(m[1], 10, 64)
		mt, _ := strconv.ParseInt(m[3], 10, 64)
		if p, err := l.store.PhotoByID(id); err != nil || p.MTime != mt {
			os.Remove(full)
		}
	}
}
