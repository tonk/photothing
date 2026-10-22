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
}

func (m *exifMeta) setGPS(lat, lon float64) {
	if lat == 0 && lon == 0 {
		return // many cameras write 0,0 for "no fix"
	}
	m.HasGPS = true
	m.Display["GPS"] = fmt.Sprintf("%.5f, %.5f", lat, lon)
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
			setStr(m.Display, "Camera make", t.Value)
		case "Model":
			setStr(m.Display, "Camera model", t.Value)
		case "LensModel":
			setStr(m.Display, "Lens", t.Value)
		case "DateTimeOriginal":
			if s, ok := t.Value.(string); ok {
				if tm, err := time.ParseInLocation("2006:01:02 15:04:05", strings.TrimRight(s, "\x00 "), time.Local); err == nil {
					m.Taken = tm
				}
			}
		case "ExposureTime":
			if r := firstRational(t.Value); r != nil && r.Numerator > 0 && r.Denominator > 0 {
				if r.Numerator == 1 || r.Numerator > r.Denominator {
					m.Display["Exposure"] = fmt.Sprintf("%d/%d s", r.Numerator, r.Denominator)
				} else {
					m.Display["Exposure"] = fmt.Sprintf("1/%.0f s", float64(r.Denominator)/float64(r.Numerator))
				}
			}
		case "FNumber":
			if r := firstRational(t.Value); r != nil && r.Denominator != 0 {
				m.Display["Aperture"] = fmt.Sprintf("f/%.1f", float64(r.Numerator)/float64(r.Denominator))
			}
		case "FocalLength":
			if r := firstRational(t.Value); r != nil && r.Denominator != 0 {
				m.Display["Focal length"] = fmt.Sprintf("%.0f mm", float64(r.Numerator)/float64(r.Denominator))
			}
		case "ISOSpeedRatings", "PhotographicSensitivity":
			if v, ok := t.Value.([]uint16); ok && len(v) > 0 {
				m.Display["ISO"] = fmt.Sprint(v[0])
			}
		case "Orientation":
			if v, ok := t.Value.([]uint16); ok && len(v) > 0 {
				m.Orientation = int(v[0])
			}
		}
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

func firstRational(v any) *exifcommon.Rational {
	if rs, ok := v.([]exifcommon.Rational); ok && len(rs) > 0 {
		return &rs[0]
	}
	return nil
}
