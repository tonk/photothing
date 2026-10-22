// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package gallery

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disintegration/imaging"
	"github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
)

func TestStaleOrientationRule(t *testing.T) {
	cases := []struct {
		name                    string
		tag, w, h, exifW, exifH int
		stale                   bool
	}{
		{"pixels already turned upright, tag left behind (the iPhone files)", 6, 4284, 5712, 5712, 4284, true},
		{"same, tag 8", 8, 3024, 4032, 4032, 3024, true},
		{"same, mirrored tags 5 and 7", 5, 3024, 4032, 4032, 3024, true},
		{"normal phone photo held upright: sensor pixels are landscape", 6, 5712, 4284, 5712, 4284, false},
		{"camera with a portrait sensor: stored and EXIF dimensions agree", 6, 3024, 4032, 3024, 4032, false},
		{"landscape pixels, tag 6, EXIF dimensions turned (not this pattern)", 6, 5712, 4284, 4284, 5712, false},
		{"tag says normal: nothing to ignore", 1, 4284, 5712, 5712, 4284, false},
		{"tag 3 is not a quarter turn", 3, 4284, 5712, 5712, 4284, false},
		{"no EXIF dimensions: cannot tell, so the tag counts", 6, 4284, 5712, 0, 0, false},
		{"square image", 6, 100, 100, 100, 100, false},
		{"EXIF dimensions unrelated", 6, 4284, 5712, 1000, 800, false},
	}
	for _, c := range cases {
		if got := staleOrientation(c.tag, c.w, c.h, c.exifW, c.exifH); got != c.stale {
			t.Errorf("%s: %v, want %v", c.name, got, c.stale)
		}
	}
}

// photoWithOrientation builds a JPEG whose pixels are `w` x `h` with the top (or, for landscape, left) half red
// and the rest blue, carrying the given Orientation tag and (when exifW > 0) the given EXIF pixel dimensions.
func photoWithOrientation(t *testing.T, w, h, tag, exifW, exifH int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			red := (h > w && y < h/2) || (w >= h && x < w/2)
			if red {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	im, _ := exifcommon.NewIfdMappingWithStandard()
	root := exif.NewIfdBuilder(im, exif.NewTagIndex(), exifcommon.IfdStandardIfdIdentity, exifcommon.EncodeDefaultByteOrder)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(root.AddStandardWithName("Orientation", []uint16{uint16(tag)}))
	if exifW > 0 {
		eb, err := exif.GetOrCreateIbFromRootIb(root, "IFD/Exif")
		must(err)
		must(eb.AddStandardWithName("PixelXDimension", []uint32{uint32(exifW)}))
		must(eb.AddStandardWithName("PixelYDimension", []uint32{uint32(exifH)}))
	}
	raw, err := exif.NewIfdByteEncoder().EncodeToExif(root)
	must(err)
	b := plain.Bytes()
	out := append([]byte{}, b[:2]...)
	return append(append(out, app1(raw)...), b[2:]...)
}

// The files that started this: pixels already upright, tag 6 still in the file, EXIF dimensions those of the
// camera. Honouring the tag turns them sideways; they must be shown as they are stored. The control cases
// have to keep turning, or ordinary phone photos would break.
func TestPhotosWithAStaleOrientationTagAreShownAsStored(t *testing.T) {
	cases := []struct {
		name                    string
		w, h, tag, exifW, exifH int
		ignored                 bool
		wantW, wantH            int        // dimensions of the photo as shown
		redAt                   [2]float64 // where the red half must be, as fractions of the shown photo
	}{
		{"stale tag (the iPhone files)", 40, 80, 6, 80, 40, true, 40, 80, [2]float64{.5, .2}},
		{"ordinary phone photo: landscape pixels, tag 6", 80, 40, 6, 80, 40, false, 40, 80, [2]float64{.5, .2}},
		{"portrait sensor: EXIF dimensions agree with the pixels", 40, 80, 6, 40, 80, false, 80, 40, [2]float64{.8, .5}},
		{"no EXIF dimensions: the tag counts", 40, 80, 6, 0, 0, false, 80, 40, [2]float64{.8, .5}},
		{"tag 1 with odd EXIF dimensions: nothing happens", 40, 80, 1, 80, 40, false, 40, 80, [2]float64{.5, .2}},
	}
	e := newEnv(t)
	for i, c := range cases {
		e.writeBytes(fmt.Sprintf("c%d.jpg", i), photoWithOrientation(t, c.w, c.h, c.tag, c.exifW, c.exifH), time.Now().Add(-time.Hour))
	}
	e.scan()
	for i, c := range cases {
		p := e.photo(fmt.Sprintf("c%d.jpg", i))
		_, noted := p.Exif[LabelOrientNote]
		if noted != c.ignored {
			t.Errorf("%s: orientation note = %v, want %v (%v)", c.name, noted, c.ignored, p.Exif)
		}
		if c.tag != 1 && p.Exif[LabelOrientation] == "" {
			t.Errorf("%s: the Orientation value is no longer shown", c.name)
		}
		if p.Width != c.wantW || p.Height != c.wantH {
			t.Errorf("%s: dimensions %dx%d, want %dx%d", c.name, p.Width, p.Height, c.wantW, c.wantH)
		}
		for _, kind := range []string{"large", "thumb"} {
			out, err := e.lib.Variant(context.Background(), p, kind)
			if err != nil {
				t.Fatal(err)
			}
			img, _ := imaging.Open(out)
			b := img.Bounds()
			if kind == "large" && (b.Dx() != c.wantW || b.Dy() != c.wantH) {
				t.Errorf("%s large: %dx%d, want %dx%d", c.name, b.Dx(), b.Dy(), c.wantW, c.wantH)
			}
			r, g, bl, _ := img.At(int(c.redAt[0]*float64(b.Dx())), int(c.redAt[1]*float64(b.Dy()))).RGBA()
			if r>>8 < 200 || g>>8 > 60 || bl>>8 > 60 {
				t.Errorf("%s %s: the red half is not where it should be", c.name, kind)
			}
		}
	}
}

// A thumbnail made by an older version (named <id>_thumb_<mtime>.jpg) was rendered with the rules of that
// version: sideways, in the case that started this. The name did not change when the rules did, so it was
// served again and again. Cached pictures now carry the rendering version, and old ones are never used.
func TestPicturesCachedByAnOlderVersionAreNotUsed(t *testing.T) {
	e := newEnv(t)
	e.writeBytes("stale.jpg", photoWithOrientation(t, 40, 80, 6, 80, 40), time.Now().Add(-time.Hour))
	e.scan()
	p := e.photo("stale.jpg")

	// what the previous version left behind: the same photo rendered sideways, under the old name
	oldName := filepath.Join(e.lib.CacheDir, fmt.Sprintf("%d_thumb_%d.jpg", p.ID, p.MTime))
	sideways := image.NewRGBA(image.Rect(0, 0, ThumbSize, ThumbSize))
	for y := 0; y < ThumbSize; y++ {
		for x := 0; x < ThumbSize; x++ {
			sideways.Set(x, y, color.RGBA{0, 0, 255, 255}) // all blue: nothing like the photo (red on top)
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, sideways, nil)
	if err := os.WriteFile(oldName, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.lib.Variant(context.Background(), p, "thumb")
	if err != nil {
		t.Fatal(err)
	}
	if out == oldName {
		t.Fatal("the picture of an older version was served")
	}
	if want := fmt.Sprintf("_r%d.jpg", RenderVersion); !strings.HasSuffix(out, want) {
		t.Errorf("cache file %s does not carry the rendering version", filepath.Base(out))
	}
	img, _ := imaging.Open(out)
	if r, _, b, _ := img.At(180, 60).RGBA(); r>>8 < 200 || b>>8 > 60 { // the top of the thumbnail is the red half
		t.Error("the new thumbnail is not rendered from the photo")
	}

	// the old file is removed, the new one stays, and the cache of a photo that is gone is cleared too
	e.lib.PruneCache()
	if _, err := os.Stat(oldName); err == nil {
		t.Error("the outdated cache file was not removed")
	}
	if _, err := os.Stat(out); err != nil {
		t.Error("the current cache file was removed")
	}
	other := filepath.Join(e.lib.CacheDir, fmt.Sprintf("%d_large_%d_r%d.jpg", p.ID, p.MTime, RenderVersion-1))
	os.WriteFile(other, buf.Bytes(), 0o644)
	e.lib.PruneCache()
	if _, err := os.Stat(other); err == nil {
		t.Error("a cache file of another rendering version was kept")
	}
}
