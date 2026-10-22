package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConf(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// clearEnv makes the tests independent of the environment they run in.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PHOTOTHING_CONFIG", "PHOTOTHING_PHOTOS", "PHOTOTHING_DATA", "PHOTOTHING_ADDR", "PHOTOTHING_SECURE_COOKIES",
		"PHOTOTHING_TRUSTED_PROXY", "PHOTOTHING_SCAN_INTERVAL", "PHOTOTHING_BACKUP_DIR"} {
		t.Setenv(k, "")
	}
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	cfg, args, err := parseArgs(nil)
	if err != nil || len(args) != 0 {
		t.Fatal(err, args)
	}
	if cfg.addr != "127.0.0.1:8080" || cfg.photos != "./photos" || cfg.data != "./data" || cfg.scanEvery != 10*time.Minute ||
		cfg.backupKeep != 7 || cfg.trustedProxy != "" || cfg.secure {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestPrecedence(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	conf := writeConf(t, dir, "a.conf", "addr = file:2\nscan-interval = 30s\ntrusted-proxy = 10.0.0.1\n")

	t.Setenv("PHOTOTHING_ADDR", "env:1")
	t.Setenv("PHOTOTHING_SCAN_INTERVAL", "1m")
	t.Setenv("PHOTOTHING_BACKUP_DIR", "/from/env")

	cfg, _, err := parseArgs([]string{"--config=" + conf})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "file:2" {
		t.Errorf("file should beat environment: addr = %s", cfg.addr)
	}
	if cfg.scanEvery != 30*time.Second {
		t.Errorf("file should beat environment: scan-interval = %s", cfg.scanEvery)
	}
	if cfg.backupDir != "/from/env" {
		t.Errorf("environment should beat the default when the file is silent: backup-dir = %s", cfg.backupDir)
	}
	if cfg.trustedProxy != "10.0.0.1" {
		t.Errorf("trusted-proxy = %s", cfg.trustedProxy)
	}

	cfg, _, err = parseArgs([]string{"-addr", "cli:3", "--config", conf, "serve"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "cli:3" {
		t.Errorf("command line should beat the file: addr = %s", cfg.addr)
	}
	if cfg.scanEvery != 30*time.Second {
		t.Errorf("other file settings should still apply: %s", cfg.scanEvery)
	}

	// the file can also be named in the environment
	t.Setenv("PHOTOTHING_CONFIG", conf)
	if cfg, _, err = parseArgs(nil); err != nil || cfg.addr != "file:2" {
		t.Errorf("PHOTOTHING_CONFIG: %v %+v", err, cfg)
	}
}

func TestConfigFileSyntaxAndPaths(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	conf := writeConf(t, dir, "x.conf", "\xef\xbb\xbf# an instance\n\n; another comment style\n"+
		"photos   =   fotos\n"+
		"data = \"state dir\"\n"+
		"backup-dir = /abs/backups\n"+
		"secure-cookies = yes\n"+
		"backup-keep=3\n"+
		"backup-every = 12h\n"+
		"addr = '0.0.0.0:9000'\n")
	cfg, _, err := parseArgs([]string{"--config=" + conf})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.photos != filepath.Join(dir, "fotos") || cfg.data != filepath.Join(dir, "state dir") {
		t.Errorf("relative paths must be relative to the file: %s, %s", cfg.photos, cfg.data)
	}
	if cfg.backupDir != "/abs/backups" {
		t.Errorf("absolute path changed: %s", cfg.backupDir)
	}
	if !cfg.secure || cfg.backupKeep != 3 || cfg.backupEvery != 12*time.Hour || cfg.addr != "0.0.0.0:9000" {
		t.Errorf("values not applied: %+v", cfg)
	}
	// relative paths given on the command line stay relative to the working directory
	cfg, _, _ = parseArgs([]string{"--config=" + conf, "-photos", "elsewhere"})
	if cfg.photos != "elsewhere" {
		t.Errorf("command-line path was rewritten: %s", cfg.photos)
	}
	// "no" and "off" switch booleans off
	off := writeConf(t, dir, "off.conf", "secure-cookies = off\n")
	t.Setenv("PHOTOTHING_SECURE_COOKIES", "1")
	if cfg, _, err = parseArgs([]string{"--config=" + off}); err != nil || cfg.secure {
		t.Errorf("secure-cookies = off: %v %v", err, cfg.secure)
	}
}

func TestConfigFileErrors(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	cases := []struct{ name, text, want string }{
		{"unknown key", "addr = :1\nlisten = :2\n", "x.conf:2: unknown setting \"listen\""},
		{"no equals sign", "addr :1\n", "line 1: expected"},
		{"duplicate key", "addr = :1\n# c\naddr = :2\n", "line 3: \"addr\" was already set on line 1"},
		{"bad duration", "\nscan-interval = soon\n", "x.conf:2: invalid value \"soon\" for \"scan-interval\""},
		{"bad number", "backup-keep = many\n", "x.conf:1: invalid value \"many\" for \"backup-keep\""},
		{"bad bool", "secure-cookies = maybe\n", "x.conf:1: invalid value \"maybe\""},
		{"config inside config", "config = other.conf\n", "cannot be set inside a configuration file"},
		{"empty key", "= value\n", "missing key"},
	}
	for _, c := range cases {
		p := writeConf(t, dir, "x.conf", c.text)
		_, _, err := parseArgs([]string{"--config=" + p})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want it to contain %q", c.name, err, c.want)
		}
	}
	if _, _, err := parseArgs([]string{"--config=" + filepath.Join(dir, "missing.conf")}); err == nil {
		t.Error("a missing configuration file must be an error")
	}
	if _, _, err := parseArgs([]string{"-no-such-flag"}); err == nil {
		t.Error("unknown flag accepted")
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	old := flagOutput
	flagOutput = &buf
	defer func() { flagOutput = old }()
	_, _, err := parseArgs([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v", err)
	}
	for _, want := range []string{"usage: photothing", "-config", "-trusted-proxy", "serve"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

// Several instances: each has its own file, and nothing leaks from one into the other.
func TestTwoInstances(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	a := writeConf(t, dir, "familie.conf", "photos = /srv/fotos/familie\ndata = /var/lib/photothing/familie\naddr = 127.0.0.1:8081\ntrusted-proxy = 127.0.0.1\n")
	b := writeConf(t, dir, "werk.conf", "photos = /srv/fotos/werk\ndata = /var/lib/photothing/werk\naddr = 127.0.0.1:8082\n")
	ca, _, err := parseArgs([]string{"--config=" + a})
	if err != nil {
		t.Fatal(err)
	}
	cb, _, err := parseArgs([]string{"--config=" + b})
	if err != nil {
		t.Fatal(err)
	}
	if ca.addr == cb.addr || ca.data == cb.data || ca.photos == cb.photos {
		t.Errorf("instances share settings:\n%+v\n%+v", ca, cb)
	}
	if ca.trustedProxy != "127.0.0.1" || cb.trustedProxy != "" {
		t.Errorf("trusted-proxy leaked between instances: %q / %q", ca.trustedProxy, cb.trustedProxy)
	}
	// the config command shows what would be used
	if got := strings.Join(ca.lines(), "\n"); !strings.Contains(got, "addr = 127.0.0.1:8081") || !strings.Contains(got, "config = "+a) {
		t.Errorf("lines: %s", got)
	}
	if err := configCmd(ca); err != nil {
		t.Errorf("config command: %v", err)
	}
	ca.trustedProxy = "nginx"
	if err := configCmd(ca); err == nil {
		t.Error("config command accepted an invalid trusted-proxy")
	}
}

func TestDataDirectoryLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockDataDir(dir); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("second lock: %v", err)
	}
	// a different instance, with its own directory, is not affected
	other, err := lockDataDir(t.TempDir())
	if err != nil {
		t.Errorf("independent directory: %v", err)
	} else {
		other()
	}
	unlock()
	again, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
	// restore refuses to run while a server holds the lock
	held, _ := lockDataDir(dir)
	defer held()
	if err := run(config{data: dir}, []string{"restore", "nothing.tar.gz"}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("restore while the server runs: %v", err)
	}
}

// The example shipped in contrib/ must stay valid, and say what its comments say.
func TestExampleConfigIsValid(t *testing.T) {
	clearEnv(t)
	cfg, _, err := parseArgs([]string{"--config=contrib/photothing.conf.example"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:8081" || cfg.trustedProxy != "127.0.0.1" || cfg.scanEvery != 10*time.Minute || cfg.backupKeep != 7 {
		t.Errorf("example parsed to %+v", cfg)
	}
	if err := configCmd(cfg); err != nil {
		t.Error(err)
	}
}
