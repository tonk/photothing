package gallery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// fileType describes how a file extension is handled.
type fileType struct {
	Kind   string // "image" or "video"
	Native bool   // decoded in pure Go; otherwise an external tool is needed
	Tool   string // "heif", "raw" or "video" when !Native
	Mime   string
	Inline bool // safe to display inline in a browser
}

var fileTypes = map[string]fileType{
	".jpg":  {"image", true, "", "image/jpeg", true},
	".jpeg": {"image", true, "", "image/jpeg", true},
	".png":  {"image", true, "", "image/png", true},
	".gif":  {"image", true, "", "image/gif", true},
	".webp": {"image", true, "", "image/webp", true},
	".heic": {"image", false, "heif", "image/heic", false},
	".heif": {"image", false, "heif", "image/heif", false},
	".dng":  {"image", false, "raw", "application/octet-stream", false},
	".cr2":  {"image", false, "raw", "application/octet-stream", false},
	".cr3":  {"image", false, "raw", "application/octet-stream", false},
	".nef":  {"image", false, "raw", "application/octet-stream", false},
	".arw":  {"image", false, "raw", "application/octet-stream", false},
	".orf":  {"image", false, "raw", "application/octet-stream", false},
	".rw2":  {"image", false, "raw", "application/octet-stream", false},
	".raf":  {"image", false, "raw", "application/octet-stream", false},
	".mp4":  {"video", false, "video", "video/mp4", true},
	".m4v":  {"video", false, "video", "video/mp4", true},
	".webm": {"video", false, "video", "video/webm", true},
	".mov":  {"video", false, "video", "video/quicktime", true},
}

func typeOf(name string) (fileType, bool) {
	ft, ok := fileTypes[strings.ToLower(filepath.Ext(name))]
	return ft, ok
}

// MimeFor returns the content type of a media file and whether browsers may show it inline.
func MimeFor(name string) (mime string, inline bool) {
	if ft, ok := typeOf(name); ok {
		return ft.Mime, ft.Inline
	}
	return "application/octet-stream", false
}

// Tools are the optional external programs that add format support.
type Tools struct {
	FFmpeg, FFprobe, Heif, Exiftool string // full paths; empty when not installed
}

func DetectTools() Tools {
	find := func(n string) string { p, _ := exec.LookPath(n); return p }
	return Tools{FFmpeg: find("ffmpeg"), FFprobe: find("ffprobe"), Heif: find("heif-convert"), Exiftool: find("exiftool")}
}

// Has reports whether the tool group ("heif", "raw", "video") is usable.
func (t Tools) Has(group string) bool {
	switch group {
	case "heif":
		return t.Heif != ""
	case "raw":
		return t.Exiftool != ""
	case "video":
		return t.FFmpeg != "" && t.FFprobe != ""
	}
	return true
}

func (t Tools) String() string {
	var have []string
	for _, g := range []string{"heif", "raw", "video"} {
		if t.Has(g) {
			have = append(have, g)
		}
	}
	if len(have) == 0 {
		return "none (JPEG, PNG, GIF and WebP only)"
	}
	return strings.Join(have, ", ")
}

var errMissingTool = errors.New("required tool not installed")

func run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, msg)
	}
	return out.Bytes(), nil
}

// toJPEG renders a non-native file (HEIC, RAW preview, video frame) to a temporary JPEG.
// The caller must call cleanup.
func (l *Library) toJPEG(ctx context.Context, abs string, ft fileType) (jpg string, cleanup func(), err error) {
	if !l.Tools.Has(ft.Tool) {
		return "", func() {}, errMissingTool
	}
	dir, err := os.MkdirTemp(l.CacheDir, "tmp-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	out := filepath.Join(dir, "out.jpg")
	switch ft.Tool {
	case "heif":
		if _, err = run(ctx, 60*time.Second, l.Tools.Heif, "-q", "92", abs, out); err == nil {
			if _, serr := os.Stat(out); serr != nil { // multi-image files get a numeric suffix
				m, _ := filepath.Glob(filepath.Join(dir, "out*.jpg"))
				if len(m) == 0 {
					err = errors.New("heif-convert produced no output")
				} else {
					out = m[0]
				}
			}
		}
	case "raw":
		for _, tag := range []string{"-JpgFromRaw", "-PreviewImage", "-ThumbnailImage"} {
			var b []byte
			b, err = run(ctx, 60*time.Second, l.Tools.Exiftool, "-b", tag, abs)
			if err == nil && len(b) > 1000 {
				err = os.WriteFile(out, b, 0o600)
				break
			}
			err = errors.New("no embedded preview in raw file")
		}
	case "video":
		// Try one second in (skips black first frames); fall back to the first frame for very short clips.
		for _, ss := range []string{"1", "0"} {
			_, err = run(ctx, 60*time.Second, l.Tools.FFmpeg, "-nostdin", "-loglevel", "error", "-ss", ss, "-i", abs,
				"-frames:v", "1", "-an", "-y", out)
			if st, serr := os.Stat(out); err == nil && serr == nil && st.Size() > 0 {
				break
			}
			if err == nil {
				err = errors.New("ffmpeg produced no frame")
			}
		}
	}
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return out, cleanup, nil
}

// exiftoolMeta reads camera data with exiftool (used for HEIC and RAW files).
func (l *Library) exiftoolMeta(ctx context.Context, abs string) exifMeta {
	m := exifMeta{Display: map[string]string{}}
	if l.Tools.Exiftool == "" {
		return m
	}
	b, err := run(ctx, 30*time.Second, l.Tools.Exiftool, "-json", "-n", "-DateTimeOriginal", "-Make", "-Model", "-LensModel",
		"-ExposureTime", "-FNumber", "-FocalLength", "-FocalLengthIn35mmFormat", "-ISO", "-GPSLatitude", "-GPSLongitude",
		"-GPSAltitude", "-GPSImgDirection", "-Flash", "-WhiteBalance", "-MeteringMode", "-ExposureProgram", "-ExposureMode",
		"-SceneCaptureType", "-ColorSpace", "-ExposureCompensation", "-Software", "-Artist", "-Copyright", "-ImageDescription", abs)
	if err != nil {
		return m
	}
	var arr []map[string]any
	if json.Unmarshal(b, &arr) != nil || len(arr) == 0 {
		return m
	}
	r := arr[0]
	str := func(k string) string { s, _ := r[k].(string); return strings.TrimSpace(s) }
	num := func(k string) (float64, bool) { f, ok := r[k].(float64); return f, ok }
	m.put(LabelMake, cleanString(str("Make"), 100))
	m.put(LabelModel, cleanString(str("Model"), 100))
	m.put(LabelLens, cleanString(str("LensModel"), 100))
	m.put(LabelSoftware, cleanString(str("Software"), 100))
	m.put(LabelArtist, cleanString(str("Artist"), 100))
	m.put(LabelCopyright, cleanString(str("Copyright"), 200))
	m.put(LabelDescription, cleanString(str("ImageDescription"), 200))
	if s := str("DateTimeOriginal"); len(s) >= 19 {
		if t, err := time.ParseInLocation("2006:01:02 15:04:05", s[:19], time.Local); err == nil {
			m.Taken = t
		}
	}
	if f, ok := num("ExposureTime"); ok && f > 0 {
		if f < 1 {
			m.Display[LabelExposure] = fmt.Sprintf("1/%.0f s", 1/f)
		} else {
			m.Display[LabelExposure] = fmt.Sprintf("%.1f s", f)
		}
	}
	if f, ok := num("FNumber"); ok {
		m.Display[LabelAperture] = fmt.Sprintf("f/%.1f", f)
	}
	if f, ok := num("FocalLength"); ok {
		m.Display[LabelFocalLength] = fmt.Sprintf("%.0f mm", f)
	}
	if f, ok := num("ISO"); ok {
		m.Display[LabelISO] = strconv.Itoa(int(f))
	}
	if f, ok := num("FocalLengthIn35mmFormat"); ok && f > 0 {
		m.Display[LabelFocal35] = fmt.Sprintf("%.0f mm", f)
	}
	if f, ok := num("ExposureCompensation"); ok {
		m.Display[LabelExposureComp] = evText(f)
	}
	if f, ok := num("Flash"); ok {
		m.Display[LabelFlash] = flashText(int(f))
	}
	for _, e := range []struct {
		key, label string
		table      map[int]string
	}{{"WhiteBalance", LabelWhiteBalance, whiteBalances}, {"MeteringMode", LabelMetering, meteringModes},
		{"ExposureProgram", LabelProgram, programs}, {"ExposureMode", LabelExposureMode, exposureModes},
		{"SceneCaptureType", LabelScene, sceneTypes}, {"ColorSpace", LabelColorSpace, colorSpaces}} {
		if f, ok := num(e.key); ok {
			m.enum(e.label, e.table, int(f))
		}
	}
	if f, ok := num("GPSAltitude"); ok {
		m.Display[LabelGPSAltitude] = metersText(f)
	}
	if f, ok := num("GPSImgDirection"); ok {
		m.Display[LabelGPSDirection] = fmt.Sprintf("%.0f°", f)
	}
	lat, ok1 := num("GPSLatitude")
	lon, ok2 := num("GPSLongitude")
	if ok1 && ok2 {
		m.setGPS(lat, lon)
	}
	return m
}

type videoInfo struct {
	W, H     int
	Duration float64
	Codec    string
	Created  time.Time
}

func (l *Library) probeVideo(ctx context.Context, abs string) (videoInfo, error) {
	var vi videoInfo
	b, err := run(ctx, 30*time.Second, l.Tools.FFprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", abs)
	if err != nil {
		return vi, err
	}
	var res struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Tags      map[string]string
			SideData  []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return vi, err
	}
	found := false
	for _, s := range res.Streams {
		if s.CodecType != "video" || s.Width == 0 {
			continue
		}
		found = true
		vi.W, vi.H, vi.Codec = s.Width, s.Height, s.CodecName
		rot := 0.0
		if v, err := strconv.ParseFloat(s.Tags["rotate"], 64); err == nil {
			rot = v
		}
		for _, sd := range s.SideData {
			if sd.Rotation != 0 {
				rot = sd.Rotation
			}
		}
		if r := int(rot); r%180 != 0 { // 90 or 270 degrees: portrait video
			vi.W, vi.H = vi.H, vi.W
		}
		break
	}
	if !found {
		return vi, errors.New("no video stream")
	}
	vi.Duration, _ = strconv.ParseFloat(res.Format.Duration, 64)
	if t, err := time.Parse(time.RFC3339Nano, res.Format.Tags["creation_time"]); err == nil {
		vi.Created = t
	}
	return vi, nil
}

func formatDuration(sec float64) string {
	s := int(sec + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
