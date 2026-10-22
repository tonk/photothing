package gallery

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
)

// exifBlock builds an EXIF block with a typical set of camera, exposure and location tags.
func exifBlock(t *testing.T, belowSea bool) []byte {
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
	rat := func(n, d uint32) []exifcommon.Rational { return []exifcommon.Rational{{Numerator: n, Denominator: d}} }

	must(root.AddStandardWithName("Make", "ACME"))
	must(root.AddStandardWithName("Model", "Shutter 9000"))
	must(root.AddStandardWithName("Orientation", []uint16{6}))
	must(root.AddStandardWithName("Software", "PhotoEdit 4.2"))
	must(root.AddStandardWithName("Artist", "Ton Kersten"))
	must(root.AddStandardWithName("Copyright", "(c) Ton Kersten\x00Studio X"))
	must(root.AddStandardWithName("ImageDescription", "Harbour at dusk"))

	eb, err := exif.GetOrCreateIbFromRootIb(root, "IFD/Exif")
	must(err)
	must(eb.AddStandardWithName("DateTimeOriginal", "2021:07:04 12:30:45"))
	must(eb.AddStandardWithName("ExposureTime", rat(1, 250)))
	must(eb.AddStandardWithName("FNumber", rat(28, 10)))
	must(eb.AddStandardWithName("FocalLength", rat(35, 1)))
	must(eb.AddStandardWithName("FocalLengthIn35mmFilm", []uint16{52}))
	must(eb.AddStandardWithName("ISOSpeedRatings", []uint16{200}))
	must(eb.AddStandardWithName("ExposureBiasValue", []exifcommon.SignedRational{{Numerator: -7, Denominator: 10}}))
	must(eb.AddStandardWithName("ExposureProgram", []uint16{3}))
	must(eb.AddStandardWithName("ExposureMode", []uint16{0}))
	must(eb.AddStandardWithName("MeteringMode", []uint16{5}))
	must(eb.AddStandardWithName("Flash", []uint16{9})) // fired, compulsory mode
	must(eb.AddStandardWithName("WhiteBalance", []uint16{1}))
	must(eb.AddStandardWithName("SceneCaptureType", []uint16{3}))
	must(eb.AddStandardWithName("ColorSpace", []uint16{1}))

	gb, err := exif.GetOrCreateIbFromRootIb(root, "IFD/GPSInfo")
	must(err)
	must(gb.AddStandardWithName("GPSLatitudeRef", "N"))
	must(gb.AddStandardWithName("GPSLatitude", []exifcommon.Rational{{Numerator: 52, Denominator: 1}, {Numerator: 30, Denominator: 1}, {Numerator: 0, Denominator: 1}}))
	must(gb.AddStandardWithName("GPSLongitudeRef", "W"))
	must(gb.AddStandardWithName("GPSLongitude", []exifcommon.Rational{{Numerator: 4, Denominator: 1}, {Numerator: 54, Denominator: 1}, {Numerator: 0, Denominator: 1}}))
	ref := byte(0)
	if belowSea {
		ref = 1
	}
	must(gb.AddStandardWithName("GPSAltitudeRef", []byte{ref}))
	must(gb.AddStandardWithName("GPSAltitude", rat(1234, 100)))
	must(gb.AddStandardWithName("GPSImgDirection", rat(2705, 10)))

	raw, err := exif.NewIfdByteEncoder().EncodeToExif(root)
	must(err)
	return raw
}

// app1 wraps an EXIF block in a JPEG APP1 segment.
func app1(raw []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xE1})
	binary.Write(&b, binary.BigEndian, uint16(2+6+len(raw)))
	b.WriteString("Exif\x00\x00")
	b.Write(raw)
	return b.Bytes()
}

// jpegWithExif wraps an EXIF block in a minimal JPEG (SOI + APP1 + EOI): enough for readExif.
func jpegWithExif(t *testing.T, belowSea bool) []byte {
	t.Helper()
	out := []byte{0xFF, 0xD8}
	out = append(out, app1(exifBlock(t, belowSea))...)
	return append(out, 0xFF, 0xD9)
}

// decodableJPEGWithExif is a real, decodable image with the EXIF block inserted after the SOI marker.
func decodableJPEGWithExif(t *testing.T, w, h int, belowSea bool) []byte {
	t.Helper()
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	b := plain.Bytes()
	out := append([]byte{}, b[:2]...)
	out = append(out, app1(exifBlock(t, belowSea))...)
	return append(out, b[2:]...)
}

func TestReadExif(t *testing.T) {
	m := readExif(bytes.NewReader(jpegWithExif(t, false)))
	want := map[string]string{
		LabelMake: "ACME", LabelModel: "Shutter 9000",
		LabelExposure: "1/250 s", LabelAperture: "f/2.8", LabelFocalLength: "35 mm", LabelFocal35: "52 mm", LabelISO: "200",
		LabelExposureComp: "-0.7 EV", LabelProgram: "Aperture priority", LabelExposureMode: "Auto", LabelMetering: "Pattern",
		LabelFlash: "Fired", LabelWhiteBalance: "Manual", LabelScene: "Night", LabelColorSpace: "sRGB",
		LabelSoftware: "PhotoEdit 4.2", LabelArtist: "Ton Kersten", LabelCopyright: "(c) Ton Kersten / Studio X",
		LabelDescription: "Harbour at dusk",
		LabelGPS:         "52.50000, -4.90000", LabelGPSAltitude: "12 m", LabelGPSDirection: "270°",
	}
	for k, v := range want {
		if m.Display[k] != v {
			t.Errorf("%s = %q, want %q", k, m.Display[k], v)
		}
	}
	for k := range m.Display {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected field %q = %q", k, m.Display[k])
		}
	}
	if m.Orientation != 6 {
		t.Errorf("orientation = %d, want 6", m.Orientation)
	}
	if got := m.Taken.Format(time.DateTime); got != "2021-07-04 12:30:45" {
		t.Errorf("taken = %s", got)
	}
	// every label that can be produced is part of the display order
	known := map[string]bool{}
	for _, k := range ExifOrder {
		known[k] = true
	}
	for k := range m.Display {
		if !known[k] {
			t.Errorf("%q is not in ExifOrder, so it would be listed last and untranslated", k)
		}
	}
}

func TestAltitudeBelowSeaLevel(t *testing.T) {
	m := readExif(bytes.NewReader(jpegWithExif(t, true)))
	if got := m.Display[LabelGPSAltitude]; got != "-12 m" {
		t.Errorf("altitude = %q, want -12 m", got)
	}
}

func TestReadExifMissing(t *testing.T) {
	m := readExif(bytes.NewReader([]byte("not a jpeg")))
	if len(m.Display) != 0 || !m.Taken.IsZero() {
		t.Errorf("expected empty result, got %+v", m)
	}
}

func TestCodedValues(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"flash fired", flashText(1), "Fired"},
		{"flash fired, return detected", flashText(0x19), "Fired"},
		{"flash off", flashText(0), "Did not fire"},
		{"flash suppressed", flashText(0x10), "Did not fire"},
		{"+1", evText(1), "+1.0 EV"},
		{"-1.3", evText(-1.33), "-1.3 EV"},
		{"zero", evText(0), "0 EV"},
		{"almost zero", evText(0.04), "0 EV"},
		{"meters", metersText(1234.4), "1234 m"},
		{"clean", cleanString("a\x00b\x00\x00  c ", 50), "a / b / c"},
		{"clean control characters", cleanString("x\ty\nz", 50), "x / y / z"},
		{"clean limit", cleanString(strings.Repeat("é", 300), 10), strings.Repeat("é", 10)},
		{"clean empty", cleanString("\x00\x00", 10), ""},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
	var m exifMeta
	m.Display = map[string]string{}
	m.enum(LabelMetering, meteringModes, 99) // unknown codes are not shown
	m.enum(LabelMetering, meteringModes, 0)
	if len(m.Display) != 0 {
		t.Errorf("unknown code produced %v", m.Display)
	}
}

// The texts of the coded values and the display order must agree, or the page would show an
// untranslated or unordered field.
func TestVocabulary(t *testing.T) {
	order := map[string]bool{}
	for _, k := range ExifOrder {
		if order[k] {
			t.Errorf("%q appears twice in ExifOrder", k)
		}
		order[k] = true
	}
	for label, texts := range ExifValueTexts() {
		if !order[label] {
			t.Errorf("%q has value texts but is not in ExifOrder", label)
		}
		if len(texts) == 0 {
			t.Errorf("%q has no value texts", label)
		}
	}
	for k := range ExifSensitive {
		if !order[k] {
			t.Errorf("sensitive field %q is not in ExifOrder", k)
		}
	}
}
