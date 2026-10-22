package gallery

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
)

// jpegWithExif wraps an EXIF block in a minimal JPEG (SOI + APP1 + EOI).
func jpegWithExif(t *testing.T) []byte {
	t.Helper()
	im, err := exifcommon.NewIfdMappingWithStandard()
	if err != nil {
		t.Fatal(err)
	}
	ti := exif.NewTagIndex()
	root := exif.NewIfdBuilder(im, ti, exifcommon.IfdStandardIfdIdentity, exifcommon.EncodeDefaultByteOrder)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(root.AddStandardWithName("Make", "ACME"))
	must(root.AddStandardWithName("Model", "Shutter 9000"))
	must(root.AddStandardWithName("Orientation", []uint16{6}))
	eb, err := exif.GetOrCreateIbFromRootIb(root, "IFD/Exif")
	must(err)
	must(eb.AddStandardWithName("DateTimeOriginal", "2021:07:04 12:30:45"))
	must(eb.AddStandardWithName("ExposureTime", []exifcommon.Rational{{Numerator: 1, Denominator: 250}}))
	must(eb.AddStandardWithName("FNumber", []exifcommon.Rational{{Numerator: 28, Denominator: 10}}))
	must(eb.AddStandardWithName("FocalLength", []exifcommon.Rational{{Numerator: 35, Denominator: 1}}))
	must(eb.AddStandardWithName("ISOSpeedRatings", []uint16{200}))
	gb, err := exif.GetOrCreateIbFromRootIb(root, "IFD/GPSInfo")
	must(err)
	must(gb.AddStandardWithName("GPSLatitudeRef", "N"))
	must(gb.AddStandardWithName("GPSLatitude", []exifcommon.Rational{{Numerator: 52, Denominator: 1}, {Numerator: 30, Denominator: 1}, {Numerator: 0, Denominator: 1}}))
	must(gb.AddStandardWithName("GPSLongitudeRef", "W"))
	must(gb.AddStandardWithName("GPSLongitude", []exifcommon.Rational{{Numerator: 4, Denominator: 1}, {Numerator: 54, Denominator: 1}, {Numerator: 0, Denominator: 1}}))
	raw, err := exif.NewIfdByteEncoder().EncodeToExif(root)
	must(err)

	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	binary.Write(&b, binary.BigEndian, uint16(2+6+len(raw)))
	b.WriteString("Exif\x00\x00")
	b.Write(raw)
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

func TestReadExif(t *testing.T) {
	m := readExif(bytes.NewReader(jpegWithExif(t)))
	want := map[string]string{
		"Camera make": "ACME", "Camera model": "Shutter 9000", "Exposure": "1/250 s",
		"Aperture": "f/2.8", "Focal length": "35 mm", "ISO": "200", "GPS": "52.50000, -4.90000",
	}
	for k, v := range want {
		if m.Display[k] != v {
			t.Errorf("%s = %q, want %q", k, m.Display[k], v)
		}
	}
	if m.Orientation != 6 {
		t.Errorf("orientation = %d, want 6", m.Orientation)
	}
	if got := m.Taken.Format(time.DateTime); got != "2021-07-04 12:30:45" {
		t.Errorf("taken = %s", got)
	}
}

func TestReadExifMissing(t *testing.T) {
	m := readExif(bytes.NewReader([]byte("not a jpeg")))
	if len(m.Display) != 0 || !m.Taken.IsZero() {
		t.Errorf("expected empty result, got %+v", m)
	}
}
