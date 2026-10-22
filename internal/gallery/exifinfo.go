// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package gallery

import (
	"fmt"
	"sort"
	"strings"
)

// The photo information shown on the photo page. Labels and the value texts of the enumerated fields
// are English; the web layer translates them. They are defined here once, so that the readers, the
// display order, the privacy rules and the translations cannot drift apart.
const (
	LabelMake         = "Camera make"
	LabelModel        = "Camera model"
	LabelLens         = "Lens"
	LabelFocalLength  = "Focal length"
	LabelFocal35      = "Focal length (35 mm equivalent)"
	LabelAperture     = "Aperture"
	LabelExposure     = "Exposure"
	LabelISO          = "ISO"
	LabelExposureComp = "Exposure compensation"
	LabelProgram      = "Exposure program"
	LabelExposureMode = "Exposure mode"
	LabelMetering     = "Metering mode"
	LabelFlash        = "Flash"
	LabelWhiteBalance = "White balance"
	LabelScene        = "Scene type"
	LabelColorSpace   = "Color space"
	LabelSoftware     = "Software"
	LabelArtist       = "Artist"
	LabelCopyright    = "Copyright"
	LabelDescription  = "Description"
	LabelGPS          = "GPS"
	LabelGPSAltitude  = "GPS altitude"
	LabelGPSDirection = "GPS direction"
	LabelDuration     = "Duration"
	LabelCodec        = "Codec"
)

// ExifOrder is the order in which the fields are shown.
var ExifOrder = []string{
	LabelMake, LabelModel, LabelLens,
	LabelFocalLength, LabelFocal35, LabelAperture, LabelExposure, LabelISO, LabelExposureComp,
	LabelProgram, LabelExposureMode, LabelMetering, LabelFlash, LabelWhiteBalance, LabelScene, LabelColorSpace,
	LabelSoftware, LabelArtist, LabelCopyright, LabelDescription,
	LabelGPS, LabelGPSAltitude, LabelGPSDirection,
	LabelDuration, LabelCodec,
}

// ExifSensitive lists the fields that reveal where a photo was taken. Like the original file, which
// carries them, they are shown only to users who may download originals.
var ExifSensitive = map[string]bool{LabelGPS: true, LabelGPSAltitude: true, LabelGPSDirection: true}

// Value tables of the fields whose EXIF value is a code (EXIF 2.32, tables for each tag).
var (
	whiteBalances = map[int]string{0: "Auto", 1: "Manual"}
	meteringModes = map[int]string{1: "Average", 2: "Center-weighted", 3: "Spot", 4: "Multi-spot", 5: "Pattern", 6: "Partial"}
	programs      = map[int]string{1: "Manual", 2: "Normal program", 3: "Aperture priority", 4: "Shutter priority",
		5: "Creative", 6: "Action", 7: "Portrait", 8: "Landscape"}
	exposureModes = map[int]string{0: "Auto", 1: "Manual", 2: "Auto bracket"}
	sceneTypes    = map[int]string{0: "Standard", 1: "Landscape", 2: "Portrait", 3: "Night"}
	colorSpaces   = map[int]string{1: "sRGB", 0xFFFF: "Uncalibrated"}
)

const (
	flashFired    = "Fired"
	flashNotFired = "Did not fire"
)

// flashText reads bit 0 of the EXIF Flash value: whether the flash fired.
func flashText(v int) string {
	if v&1 == 1 {
		return flashFired
	}
	return flashNotFired
}

// ExifValueTexts lists, per label, the texts its value can have. Fields that are not listed (names,
// numbers) are shown as they are.
func ExifValueTexts() map[string][]string {
	of := func(m map[int]string) []string {
		var out []string
		for _, v := range m {
			out = append(out, v)
		}
		sort.Strings(out)
		return out
	}
	return map[string][]string{
		LabelFlash:        {flashFired, flashNotFired},
		LabelWhiteBalance: of(whiteBalances),
		LabelMetering:     of(meteringModes),
		LabelProgram:      of(programs),
		LabelExposureMode: of(exposureModes),
		LabelScene:        of(sceneTypes),
		LabelColorSpace:   of(colorSpaces),
	}
}

// enum stores the text for a code from one of the tables above; unknown codes are not shown.
func (m *exifMeta) enum(label string, table map[int]string, code int) {
	if s, ok := table[code]; ok {
		m.Display[label] = s
	}
}

func (m *exifMeta) put(label, value string) {
	if value != "" {
		m.Display[label] = value
	}
}

// evText formats an exposure compensation in stops: "+0.7 EV", "-1.0 EV", "0 EV".
func evText(f float64) string {
	if f > -0.05 && f < 0.05 {
		return "0 EV"
	}
	return fmt.Sprintf("%+.1f EV", f)
}

func metersText(f float64) string { return fmt.Sprintf("%.0f m", f) }

// cleanString makes a free-text EXIF value safe and short: control characters become separators
// (EXIF often holds several NUL-separated parts, e.g. photographer and editor of a copyright).
func cleanString(s string, max int) string {
	var parts []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	out := strings.Join(parts, " / ")
	if r := []rune(out); len(r) > max {
		out = string(r[:max])
	}
	return out
}
