// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package gallery

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
)

type exifMeta struct {
	Display     map[string]string // label -> value, shown on the photo page
	Taken       time.Time
	Orientation int
	HasGPS      bool
	// PixelX and PixelY are the image dimensions the camera wrote into the EXIF data (0 when absent).
	PixelX, PixelY int
}

// staleOrientation recognises a file whose pixels were already turned upright while the Orientation tag
// was left as it was: converters from HEIC to JPEG, and tools that rotate the pixels but copy the old
// metadata, produce them. Applying the tag to such a file turns the photo a second time.
//
// A camera records the pixels as the sensor delivers them (landscape) and sets Orientation 5-8 to say
// "turn this to see it upright", and writes the sensor's dimensions in the EXIF data, so for a normal file
// the stored pixels are landscape. A file whose stored pixels are portrait, with a tag of 5-8 and EXIF
// dimensions that are exactly the stored ones turned the other way, is a file that has been rotated since.
// Portrait pixels with EXIF dimensions that agree (a camera with a portrait sensor) are left alone.
func staleOrientation(tag, storedW, storedH, exifW, exifH int) bool {
	return tag >= 5 && tag <= 8 && storedW < storedH && exifW == storedH && exifH == storedW
}

func (m *exifMeta) setGPS(lat, lon float64) {
	if lat == 0 && lon == 0 {
		return // many cameras write 0,0 for "no fix"
	}
	m.HasGPS = true
	m.Display[LabelGPS] = fmt.Sprintf("%.5f, %.5f", lat, lon)
}

// gpsCoord converts degrees/minutes/seconds plus a hemisphere reference to signed decimal degrees.
func gpsCoord(v any, ref string) (float64, bool) {
	rs, ok := v.([]exifcommon.Rational)
	if !ok || len(rs) < 3 {
		return 0, false
	}
	f := func(r exifcommon.Rational) float64 {
		if r.Denominator == 0 {
			return 0
		}
		return float64(r.Numerator) / float64(r.Denominator)
	}
	d := f(rs[0]) + f(rs[1])/60 + f(rs[2])/3600
	if ref == "S" || ref == "W" {
		d = -d
	}
	return d, true
}

// readExif is best effort: images without (valid) EXIF simply yield an empty result.
func readExif(r io.Reader) exifMeta {
	m := exifMeta{Display: map[string]string{}}
	raw, err := exif.SearchAndExtractExifWithReader(r)
	if err != nil {
		return m
	}
	tags, _, err := exif.GetFlatExifData(raw, nil)
	if err != nil {
		return m
	}
	var latV, lonV any
	var latRef, lonRef string
	var altitude, direction float64
	var hasAltitude, hasDirection, belowSea bool
	for _, t := range tags {
		switch t.TagName {
		case "GPSLatitude":
			latV = t.Value
		case "GPSLongitude":
			lonV = t.Value
		case "GPSLatitudeRef":
			latRef, _ = t.Value.(string)
		case "GPSLongitudeRef":
			lonRef, _ = t.Value.(string)
		case "Make":
			setStr(m.Display, LabelMake, t.Value)
		case "Model":
			setStr(m.Display, LabelModel, t.Value)
		case "LensModel":
			setStr(m.Display, LabelLens, t.Value)
		case "DateTimeOriginal":
			if s, ok := t.Value.(string); ok {
				if tm, err := time.ParseInLocation("2006:01:02 15:04:05", strings.TrimRight(s, "\x00 "), time.Local); err == nil {
					m.Taken = tm
				}
			}
		case "ExposureTime":
			if r := firstRational(t.Value); r != nil && r.Numerator > 0 && r.Denominator > 0 {
				if r.Numerator == 1 || r.Numerator > r.Denominator {
					m.Display[LabelExposure] = fmt.Sprintf("%d/%d s", r.Numerator, r.Denominator)
				} else {
					m.Display[LabelExposure] = fmt.Sprintf("1/%.0f s", float64(r.Denominator)/float64(r.Numerator))
				}
			}
		case "FNumber":
			if r := firstRational(t.Value); r != nil && r.Denominator != 0 {
				m.Display[LabelAperture] = fmt.Sprintf("f/%.1f", float64(r.Numerator)/float64(r.Denominator))
			}
		case "FocalLength":
			if r := firstRational(t.Value); r != nil && r.Denominator != 0 {
				m.Display[LabelFocalLength] = fmt.Sprintf("%.0f mm", float64(r.Numerator)/float64(r.Denominator))
			}
		case "ISOSpeedRatings", "PhotographicSensitivity":
			if v, ok := t.Value.([]uint16); ok && len(v) > 0 {
				m.Display[LabelISO] = fmt.Sprint(v[0])
			}
		case "Orientation":
			if v, ok := t.Value.([]uint16); ok && len(v) > 0 {
				m.Orientation = int(v[0])
				m.enum(LabelOrientation, orientations, m.Orientation)
			}
		case "PixelXDimension":
			m.PixelX = firstUint(t.Value)
		case "PixelYDimension":
			m.PixelY = firstUint(t.Value)
		case "Flash":
			if v, ok := firstUint16(t.Value); ok {
				m.Display[LabelFlash] = flashText(v)
			}
		case "WhiteBalance":
			if v, ok := firstUint16(t.Value); ok {
				m.enum(LabelWhiteBalance, whiteBalances, v)
			}
		case "MeteringMode":
			if v, ok := firstUint16(t.Value); ok {
				m.enum(LabelMetering, meteringModes, v)
			}
		case "ExposureProgram":
			if v, ok := firstUint16(t.Value); ok {
				m.enum(LabelProgram, programs, v)
			}
		case "ExposureMode":
			if v, ok := firstUint16(t.Value); ok {
				m.enum(LabelExposureMode, exposureModes, v)
			}
		case "SceneCaptureType":
			if v, ok := firstUint16(t.Value); ok {
				m.enum(LabelScene, sceneTypes, v)
			}
		case "ColorSpace":
			if v, ok := firstUint16(t.Value); ok {
				m.enum(LabelColorSpace, colorSpaces, v)
			}
		case "FocalLengthIn35mmFilm":
			if v, ok := firstUint16(t.Value); ok && v > 0 {
				m.Display[LabelFocal35] = fmt.Sprintf("%d mm", v)
			}
		case "ExposureBiasValue":
			if rs, ok := t.Value.([]exifcommon.SignedRational); ok && len(rs) > 0 && rs[0].Denominator != 0 {
				m.Display[LabelExposureComp] = evText(float64(rs[0].Numerator) / float64(rs[0].Denominator))
			}
		case "Software":
			m.put(LabelSoftware, cleanStr(t.Value, 100))
		case "Artist":
			m.put(LabelArtist, cleanStr(t.Value, 100))
		case "Copyright":
			m.put(LabelCopyright, cleanStr(t.Value, 200))
		case "ImageDescription":
			m.put(LabelDescription, cleanStr(t.Value, 200))
		case "GPSAltitude":
			if r := firstRational(t.Value); r != nil && r.Denominator != 0 {
				altitude, hasAltitude = float64(r.Numerator)/float64(r.Denominator), true
			}
		case "GPSAltitudeRef":
			if b, ok := t.Value.([]byte); ok && len(b) > 0 {
				belowSea = b[0] == 1
			}
		case "GPSImgDirection":
			if r := firstRational(t.Value); r != nil && r.Denominator != 0 {
				direction, hasDirection = float64(r.Numerator)/float64(r.Denominator), true
			}
		}
	}
	if hasAltitude {
		if belowSea {
			altitude = -altitude
		}
		m.Display[LabelGPSAltitude] = metersText(altitude)
	}
	if hasDirection {
		m.Display[LabelGPSDirection] = fmt.Sprintf("%.0f°", direction)
	}
	lat, ok1 := gpsCoord(latV, strings.Trim(latRef, "\x00 "))
	lon, ok2 := gpsCoord(lonV, strings.Trim(lonRef, "\x00 "))
	if ok1 && ok2 {
		m.setGPS(lat, lon)
	}
	return m
}

func setStr(dst map[string]string, label string, v any) {
	if s, ok := v.(string); ok {
		if s = strings.TrimSpace(strings.Trim(s, "\x00")); s != "" {
			dst[label] = s
		}
	}
}

// firstUint reads the first value of a SHORT or LONG tag (EXIF allows both for the pixel dimensions).
func firstUint(v any) int {
	switch u := v.(type) {
	case []uint16:
		if len(u) > 0 {
			return int(u[0])
		}
	case []uint32:
		if len(u) > 0 {
			return int(u[0])
		}
	}
	return 0
}

func firstUint16(v any) (int, bool) {
	if u, ok := v.([]uint16); ok && len(u) > 0 {
		return int(u[0]), true
	}
	return 0, false
}

func cleanStr(v any, max int) string {
	if s, ok := v.(string); ok {
		return cleanString(s, max)
	}
	return ""
}

func firstRational(v any) *exifcommon.Rational {
	if rs, ok := v.([]exifcommon.Rational); ok && len(rs) > 0 {
		return &rs[0]
	}
	return nil
}
