package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"photothing/internal/web"
)

// config holds the settings of one photothing instance. Every setting is a command-line flag; most can
// also be set with an environment variable or in a configuration file (--config=FILE). Precedence, from
// weakest to strongest: built-in default, environment variable, configuration file, command-line flag.
type config struct {
	photos, data, addr string
	secure             bool
	trustedProxy       string
	scanEvery          time.Duration
	backupDir          string
	backupKeep         int
	backupEvery        time.Duration
	configFile         string
}

// flagOutput is where flag errors and usage are written (replaced in tests).
var flagOutput io.Writer = os.Stderr

// pathKeys are the settings that name files or directories. In a configuration file a relative path is
// relative to the directory of that file, so an instance can be moved or started from anywhere.
var pathKeys = map[string]bool{"photos": true, "data": true, "backup-dir": true}

func newFlagSet(cfg *config) *flag.FlagSet {
	fs := flag.NewFlagSet("photothing", flag.ContinueOnError)
	fs.SetOutput(flagOutput)
	fs.StringVar(&cfg.configFile, "config", env("PHOTOTHING_CONFIG", ""), "read settings from this file (key = value lines named like these flags)")
	fs.StringVar(&cfg.photos, "photos", env("PHOTOTHING_PHOTOS", "./photos"), "directory with photos (read-only use)")
	fs.StringVar(&cfg.data, "data", env("PHOTOTHING_DATA", "./data"), "directory for database, key and image cache")
	fs.StringVar(&cfg.addr, "addr", env("PHOTOTHING_ADDR", "127.0.0.1:8080"), "listen address")
	fs.BoolVar(&cfg.secure, "secure-cookies", os.Getenv("PHOTOTHING_SECURE_COOKIES") == "1", "always mark cookies Secure (HTTPS seen through a trusted proxy does this automatically)")
	fs.StringVar(&cfg.trustedProxy, "trusted-proxy", env("PHOTOTHING_TRUSTED_PROXY", ""), "comma-separated addresses/CIDRs of reverse proxies whose X-Forwarded-For/-Proto headers are believed")
	fs.DurationVar(&cfg.scanEvery, "scan-interval", envDuration("PHOTOTHING_SCAN_INTERVAL", 10*time.Minute), "rescan the photo directory this often (0 = only at start and from the Admin page)")
	fs.StringVar(&cfg.backupDir, "backup-dir", env("PHOTOTHING_BACKUP_DIR", ""), "write automatic backups to this directory (serve only)")
	fs.IntVar(&cfg.backupKeep, "backup-keep", 7, "number of automatic backups to keep")
	fs.DurationVar(&cfg.backupEvery, "backup-every", 24*time.Hour, "time between automatic backups")
	fs.Usage = func() { fmt.Fprint(flagOutput, usage); fs.PrintDefaults() }
	return fs
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return d
	}
	return def
}

// parseArgs reads the global flags and the configuration file they name, and returns the settings and
// the remaining arguments (the command).
func parseArgs(args []string) (config, []string, error) {
	var cfg config
	fs := newFlagSet(&cfg)
	if err := fs.Parse(args); err != nil {
		return cfg, nil, err
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if cfg.configFile != "" {
		if err := applyConfigFile(fs, cfg.configFile, explicit); err != nil {
			return cfg, nil, err
		}
	}
	return cfg, fs.Args(), nil
}

type fileEntry struct {
	key, value string
	line       int
}

// parseConfigFile reads "key = value" lines. Blank lines and lines starting with # or ; are ignored.
// A value may be wrapped in double or single quotes. Keys are the flag names.
func parseConfigFile(r io.Reader) ([]fileEntry, error) {
	var out []fileEntry
	seen := map[string]int{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf") // byte order mark
		}
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected \"key = value\"", n)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if key == "" {
			return nil, fmt.Errorf("line %d: missing key", n)
		}
		if first, dup := seen[key]; dup {
			return nil, fmt.Errorf("line %d: %q was already set on line %d", n, key, first)
		}
		seen[key] = n
		out = append(out, fileEntry{key, value, n})
	}
	return out, sc.Err()
}

// applyConfigFile sets every flag named in the file that was not given on the command line.
func applyConfigFile(fs *flag.FlagSet, path string, explicit map[string]bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := parseConfigFile(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	for _, e := range entries {
		fl := fs.Lookup(e.key)
		switch {
		case fl == nil:
			return fmt.Errorf("%s:%d: unknown setting %q", path, e.line, e.key)
		case e.key == "config":
			return fmt.Errorf("%s:%d: \"config\" cannot be set inside a configuration file", path, e.line)
		case explicit[e.key]:
			continue // the command line wins
		}
		value := e.value
		if pathKeys[e.key] && value != "" && !filepath.IsAbs(value) {
			value = filepath.Join(dir, value)
		}
		if b, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			switch strings.ToLower(value) {
			case "yes", "on":
				value = "true"
			case "no", "off":
				value = "false"
			}
		}
		if err := fs.Set(e.key, value); err != nil {
			return fmt.Errorf("%s:%d: invalid value %q for %q: %s", path, e.line, e.value, e.key, cleanFlagErr(err))
		}
	}
	return nil
}

func cleanFlagErr(err error) string {
	var ne *strconv.NumError
	if errors.As(err, &ne) {
		return ne.Err.Error()
	}
	return err.Error()
}

// validate checks the settings that can be checked without starting anything.
func (c config) validate() error {
	if _, err := web.ParseTrustedProxies(c.trustedProxy); err != nil {
		return err
	}
	if c.backupKeep < 1 {
		return errors.New("backup-keep must be at least 1 (a lower value would delete every backup)")
	}
	for name, d := range map[string]time.Duration{"scan-interval": c.scanEvery, "backup-every": c.backupEvery} {
		if d < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	return nil
}

// lines describes the effective settings, one "key = value" per line (used by the "config" command).
func (c config) lines() []string {
	return []string{
		"config = " + c.configFile,
		"photos = " + c.photos,
		"data = " + c.data,
		"addr = " + c.addr,
		"secure-cookies = " + strconv.FormatBool(c.secure),
		"trusted-proxy = " + c.trustedProxy,
		"scan-interval = " + c.scanEvery.String(),
		"backup-dir = " + c.backupDir,
		"backup-keep = " + strconv.Itoa(c.backupKeep),
		"backup-every = " + c.backupEvery.String(),
	}
}
