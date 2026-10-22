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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"

	"photothing/internal/store"
)

// metaVersion numbers what is read from a file into the database (dimensions, EXIF fields, ...). When
// a release reads more, it raises this number and the next scan reads every file again, once, so
// existing photos get the new details without anybody touching their files.
const metaVersion = 2

const metaVersionKey = "meta_version"

const (
	maxPixels = 100_000_000 // refuse decompression bombs
	ThumbSize = 360
	LargeSize = 1920
)

type Library struct {
	RootPath  string // absolute, symlinks resolved
	root      *os.Root
	CacheDir  string
	Tools     Tools
	store     *store.Store
	scanMu    sync.Mutex
	resizeSem chan struct{}
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
		resizeSem: make(chan struct{}, runtime.NumCPU())}, nil
}

// OpenOriginal opens a photo by its root-relative path; it cannot escape the root.
func (l *Library) OpenOriginal(rel string) (*os.File, error) {
	return l.root.Open(filepath.FromSlash(rel))
}

func (l *Library) abs(rel string) string { return filepath.Join(l.RootPath, filepath.FromSlash(rel)) }

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
		if meta.Orientation >= 5 && meta.Orientation <= 8 {
			ph.Width, ph.Height = ph.Height, ph.Width
		}
	case ft.Kind == "video":
		if !l.Tools.Has(ft.Tool) {
			return nil, errMissingTool
		}
		vi, err := l.probeVideo(ctx, l.abs(rel))
		if err != nil {
			return nil, err
		}
		ph.Width, ph.Height = vi.W, vi.H
		meta = exifMeta{Display: map[string]string{LabelDuration: formatDuration(vi.Duration)}, Taken: vi.Created}
		if vi.Codec != "" {
			meta.Display[LabelCodec] = vi.Codec
		}
	default: // HEIC and RAW: dimensions of the rendered JPEG, metadata from exiftool
		jpg, cleanup, err := l.toJPEG(ctx, l.abs(rel), ft)
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
		meta = l.exiftoolMeta(ctx, l.abs(rel))
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

// decode returns the image of a photo (for videos: a frame), with EXIF orientation applied.
func (l *Library) decode(ctx context.Context, p *store.Photo) (image.Image, error) {
	ft, _ := typeOf(p.Filename)
	if ft.Native {
		f, err := l.OpenOriginal(p.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return imaging.Decode(f, imaging.AutoOrientation(true))
	}
	jpg, cleanup, err := l.toJPEG(ctx, l.abs(p.Path), ft)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return imaging.Open(jpg, imaging.AutoOrientation(true))
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
		var id int64
		var kind string
		var mt int64
		if _, err := fmt.Sscanf(e.Name(), "%d_%5s_%d.jpg", &id, &kind, &mt); err != nil {
			continue
		}
		if p, err := l.store.PhotoByID(id); err != nil || p.MTime != mt {
			os.Remove(full)
		}
	}
}
