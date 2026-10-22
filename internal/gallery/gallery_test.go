package gallery

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disintegration/imaging"

	"photothing/internal/store"
)

type env struct {
	t     *testing.T
	root  string
	lib   *Library
	store *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "photos")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lib, err := Open(root, filepath.Join(dir, "cache"), st)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t, root, lib, st}
}

func (e *env) writeJPEG(rel string, w, h int, mtime time.Time) string {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(w + h), 255})
		}
	}
	f, err := os.Create(p)
	if err != nil {
		e.t.Fatal(err)
	}
	jpeg.Encode(f, img, nil)
	f.Close()
	os.Chtimes(p, mtime, mtime)
	return p
}

func (e *env) scan() ScanResult {
	e.t.Helper()
	res, err := e.lib.Scan(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) photo(rel string) *store.Photo {
	e.t.Helper()
	idx, _ := e.store.PhotoIndex()
	ent, ok := idx[rel]
	if !ok {
		e.t.Fatalf("%s not indexed", rel)
	}
	p, err := e.store.PhotoByID(ent.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func need(t *testing.T, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s not installed", tool)
	}
}

func TestScanAddUpdateRemove(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	e.writeJPEG("a/one.jpg", 40, 30, mt)
	e.writeJPEG("two.jpg", 20, 20, mt)
	os.WriteFile(filepath.Join(e.root, "notes.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(e.root, ".hidden"), 0o755)
	e.writeJPEG(".hidden/secret.jpg", 10, 10, mt)

	if r := e.scan(); r.Added != 2 || r.Skipped != 0 {
		t.Fatalf("first scan: %s", r)
	}
	if p := e.photo("a/one.jpg"); p.Dir != "a" || p.Width != 40 || p.Height != 30 || p.Kind != "image" {
		t.Errorf("unexpected photo %+v", p)
	}
	if r := e.scan(); r.Added+r.Updated+r.Moved+r.Removed != 0 {
		t.Errorf("rescan changed something: %s", r)
	}
	e.writeJPEG("two.jpg", 25, 25, mt.Add(time.Minute))
	if r := e.scan(); r.Updated != 1 {
		t.Errorf("modified file: %s", r)
	}
	os.Remove(filepath.Join(e.root, "two.jpg"))
	if r := e.scan(); r.Removed != 1 {
		t.Errorf("removed file: %s", r)
	}
}

func TestRenameKeepsIdentityAndCovers(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	e.writeJPEG("vakantie/benidorm/a.jpg", 40, 30, mt)
	e.writeJPEG("vakantie/benidorm/b.jpg", 41, 30, mt.Add(time.Second))
	e.scan()
	a := e.photo("vakantie/benidorm/a.jpg")
	e.store.SetCaption(a.ID, "the beach")
	e.store.SetTags(a.ID, []string{"beach", "sun"})
	e.store.SetCover("vakantie", a.ID)
	e.store.SetCover("vakantie/benidorm", a.ID)

	// Rename the album directory and one file in it.
	if err := os.Rename(filepath.Join(e.root, "vakantie/benidorm"), filepath.Join(e.root, "vakantie/benidorm_2012")); err != nil {
		t.Fatal(err)
	}
	r := e.scan()
	if r.Moved != 2 || r.Added != 0 || r.Removed != 0 {
		t.Fatalf("directory rename: %s", r)
	}
	got := e.photo("vakantie/benidorm_2012/a.jpg")
	if got.ID != a.ID || got.Caption != "the beach" {
		t.Errorf("identity lost: %+v", got)
	}
	if tags, _ := e.store.PhotoTags(a.ID); len(tags) != 2 {
		t.Errorf("tags lost: %v", tags)
	}
	covers, _ := e.store.Covers()
	if covers["vakantie"].PhotoID != a.ID {
		t.Error("cover of the parent album was lost")
	}
	if covers["vakantie/benidorm_2012"].PhotoID != a.ID {
		t.Errorf("cover of the renamed album did not follow the rename: %v", covers)
	}
	if _, ok := covers["vakantie/benidorm"]; ok {
		t.Error("stale cover for old album name")
	}

	// Moving to another depth drops covers of albums the photo left.
	os.MkdirAll(filepath.Join(e.root, "x"), 0o755)
	os.Rename(filepath.Join(e.root, "vakantie/benidorm_2012/a.jpg"), filepath.Join(e.root, "x/a.jpg"))
	if r := e.scan(); r.Moved != 1 {
		t.Fatalf("move: %s", r)
	}
	covers, _ = e.store.Covers()
	if len(covers) != 0 {
		t.Errorf("covers should be dropped after leaving the album: %v", covers)
	}
}

func TestIdenticalFilesAreNotGuessedAsRename(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	e.writeJPEG("a.jpg", 30, 30, mt)
	e.writeJPEG("b.jpg", 30, 30, mt) // same size and mtime as a.jpg
	e.scan()
	os.Remove(filepath.Join(e.root, "a.jpg"))
	os.Remove(filepath.Join(e.root, "b.jpg"))
	e.writeJPEG("c.jpg", 30, 30, mt)
	e.writeJPEG("d.jpg", 30, 30, mt)
	if r := e.scan(); r.Moved != 0 || r.Added != 2 || r.Removed != 2 {
		t.Errorf("ambiguous files must not be treated as renames: %s", r)
	}
}

func TestVariants(t *testing.T) {
	e := newEnv(t)
	e.writeJPEG("big.jpg", 3000, 1000, time.Now().Add(-time.Hour))
	e.scan()
	p := e.photo("big.jpg")
	for kind, check := range map[string]func(image.Point) bool{
		"thumb": func(s image.Point) bool { return s.X == ThumbSize && s.Y == ThumbSize },
		"large": func(s image.Point) bool { return s.X == LargeSize && s.Y == 640 },
	} {
		out, err := e.lib.Variant(context.Background(), p, kind)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		img, err := imaging.Open(out)
		if err != nil || !check(img.Bounds().Size()) {
			t.Errorf("%s: size %v err %v", kind, img.Bounds().Size(), err)
		}
	}
	if _, err := e.lib.Variant(context.Background(), p, "huge"); err == nil {
		t.Error("unknown variant accepted")
	}
}

func TestMissingToolsAreReportedNotFatal(t *testing.T) {
	e := newEnv(t)
	e.lib.Tools = Tools{}
	os.WriteFile(filepath.Join(e.root, "x.heic"), []byte("not really"), 0o644)
	os.WriteFile(filepath.Join(e.root, "y.mp4"), []byte("not really"), 0o644)
	r := e.scan()
	if r.Skipped != 2 || r.MissingTools["heif"] != 1 || r.MissingTools["video"] != 1 {
		t.Errorf("got %s %v", r, r.MissingTools)
	}
}

func TestWebP(t *testing.T) {
	need(t, "magick")
	e := newEnv(t)
	src := e.writeJPEG("src.jpg", 64, 48, time.Now())
	if out, err := exec.Command("magick", src, filepath.Join(e.root, "pic.webp")).CombinedOutput(); err != nil {
		t.Skipf("cannot create webp: %v %s", err, out)
	}
	os.Remove(src)
	if r := e.scan(); r.Added != 1 {
		t.Fatalf("scan: %s", r)
	}
	p := e.photo("pic.webp")
	if p.Width != 64 || p.Height != 48 {
		t.Errorf("dims %dx%d", p.Width, p.Height)
	}
	if _, err := e.lib.Variant(context.Background(), p, "thumb"); err != nil {
		t.Error(err)
	}
}

func TestVideo(t *testing.T) {
	need(t, "ffmpeg")
	need(t, "ffprobe")
	e := newEnv(t)
	out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-pix_fmt", "yuv420p", filepath.Join(e.root, "clip.mp4")).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create video: %v %s", err, out)
	}
	if r := e.scan(); r.Added != 1 || r.Skipped != 0 {
		t.Fatalf("scan: %s", r)
	}
	p := e.photo("clip.mp4")
	if !p.IsVideo() || p.Width != 320 || p.Height != 240 || p.Exif["Duration"] != "0:02" {
		t.Errorf("unexpected video %+v", p)
	}
	for _, kind := range []string{"thumb", "large"} {
		if _, err := e.lib.Variant(context.Background(), p, kind); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

func TestHEIC(t *testing.T) {
	need(t, "heif-enc")
	need(t, "heif-convert")
	e := newEnv(t)
	src := e.writeJPEG("src.jpg", 200, 100, time.Now())
	if out, err := exec.Command("heif-enc", src, "-o", filepath.Join(e.root, "shot.heic")).CombinedOutput(); err != nil {
		t.Skipf("cannot create heic: %v %s", err, out)
	}
	os.Remove(src)
	if r := e.scan(); r.Added != 1 || r.Skipped != 0 {
		t.Fatalf("scan: %s", r)
	}
	p := e.photo("shot.heic")
	if p.Kind != "image" || p.Width != 200 || p.Height != 100 {
		t.Errorf("unexpected heic %+v", p)
	}
	out, err := e.lib.Variant(context.Background(), p, "large")
	if err != nil {
		t.Fatal(err)
	}
	if img, err := imaging.Open(out); err != nil || img.Bounds().Dx() != 200 {
		t.Errorf("variant: %v %v", img.Bounds(), err)
	}
}

func (e *env) writeBytes(rel string, b []byte, mtime time.Time) string {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
	return p
}

// After an upgrade that reads more from the files, the next scan reads every file again, once, even
// though no file changed.
func TestMetadataIsReadAgainOnceAfterAnUpgrade(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	e.writeBytes("a.jpg", decodableJPEGWithExif(t, 40, 30, false), mt)
	if r := e.scan(); r.Added != 1 {
		t.Fatal(r)
	}
	if got := e.photo("a.jpg").Exif[LabelFlash]; got != "Fired" {
		t.Fatalf("flash = %q", got)
	}
	if r := e.scan(); r.Updated != 0 {
		t.Errorf("an unchanged library was read again: %s", r)
	}

	// Pretend the database was filled by a version that knew fewer fields.
	p := e.photo("a.jpg")
	p.Exif = map[string]string{LabelMake: "ACME"}
	if err := e.store.UpsertPhoto(p); err != nil {
		t.Fatal(err)
	}
	e.store.SetSetting(metaVersionKey, "1")

	if r := e.scan(); r.Updated != 1 {
		t.Fatalf("upgrade scan should read the file again: %s", r)
	}
	got := e.photo("a.jpg").Exif
	if got[LabelFlash] != "Fired" || got[LabelGPSAltitude] != "12 m" || got[LabelFocal35] != "52 mm" {
		t.Errorf("new fields were not filled in: %v", got)
	}
	if v := e.store.Setting(metaVersionKey); v != "2" {
		t.Errorf("version recorded as %q", v)
	}
	if r := e.scan(); r.Updated != 0 {
		t.Errorf("read again a second time: %s", r)
	}
	// the read-again pass must not break rename detection or removals
	os.Rename(filepath.Join(e.root, "a.jpg"), filepath.Join(e.root, "b.jpg"))
	if r := e.scan(); r.Moved != 1 || r.Removed != 0 || r.Added != 0 {
		t.Errorf("rename after upgrade: %s", r)
	}
}

// HEIC and RAW metadata comes from exiftool, which names and codes things differently from the JPEG
// reader; the result must look the same.
func TestExiftoolMetadata(t *testing.T) {
	need(t, "exiftool")
	need(t, "heif-enc")
	need(t, "heif-convert")
	e := newEnv(t)
	src := e.writeBytes("src.jpg", decodableJPEGWithExif(t, 64, 48, false), time.Now())
	if out, err := exec.Command("heif-enc", src, "-o", filepath.Join(e.root, "shot.heic")).CombinedOutput(); err != nil {
		t.Skipf("cannot create heic: %v %s", err, out)
	}
	os.Remove(src)
	if r := e.scan(); r.Added != 1 {
		t.Fatal(r)
	}
	got := e.photo("shot.heic").Exif
	if got[LabelMake] != "ACME" {
		t.Skipf("heif-enc did not carry the EXIF block over: %v", got)
	}
	want := map[string]string{
		LabelModel: "Shutter 9000", LabelExposure: "1/250 s", LabelAperture: "f/2.8", LabelFocalLength: "35 mm", LabelFocal35: "52 mm",
		LabelISO: "200", LabelExposureComp: "-0.7 EV", LabelProgram: "Aperture priority", LabelExposureMode: "Auto",
		LabelMetering: "Pattern", LabelFlash: "Fired", LabelWhiteBalance: "Manual", LabelScene: "Night", LabelColorSpace: "sRGB",
		LabelSoftware: "PhotoEdit 4.2", LabelArtist: "Ton Kersten", LabelDescription: "Harbour at dusk",
		LabelGPS: "52.50000, -4.90000", LabelGPSAltitude: "12 m", LabelGPSDirection: "270°",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if !strings.Contains(got[LabelCopyright], "Ton Kersten") {
		t.Errorf("copyright = %q", got[LabelCopyright])
	}
}
