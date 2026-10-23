// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeApt writes an apt-get that logs every call and, for the first `slow` downloads, hangs for longer than the
// attempt limit (a slow mirror). The final install must run from the cache, so it must never be slow.
func fakeApt(t *testing.T, slow int) (path, logfile string) {
	t.Helper()
	dir := t.TempDir()
	logfile = filepath.Join(dir, "calls.log")
	counter := filepath.Join(dir, "downloads")
	script := `#!/bin/sh
echo "$*" >> "` + logfile + `"
case "$*" in
  *--download-only*)
    n=$(cat "` + counter + `" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "` + counter + `"
    if [ "$n" -le ` + strconv.Itoa(slow) + ` ]; then sleep 5; fi ;;
esac
exit 0
`
	path = filepath.Join(dir, "apt-get")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, logfile
}

func runInstall(t *testing.T, apt string, attempts string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "scripts/install-optional-tools.sh")
	cmd.Env = append(os.Environ(), "APT_GET="+apt, "SUDO=", "ATTEMPTS="+attempts, "ATTEMPT_TIMEOUT=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func calls(t *testing.T, logfile string) (updates, downloads, installs int) {
	t.Helper()
	b, _ := os.ReadFile(logfile)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		switch {
		case strings.Contains(l, " update"):
			updates++
		case strings.Contains(l, "--download-only"):
			downloads++
		case strings.Contains(l, " install"):
			installs++
		}
	}
	return
}

func TestInstallOptionalToolsSurvivesASlowMirror(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout not installed")
	}
	// the mirror is slow twice: two attempts are cut off, the third gets through, and then the packages are installed
	apt, logfile := fakeApt(t, 2)
	out, err := runInstall(t, apt, "5")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if u, d, i := calls(t, logfile); d != 3 || i != 1 || u != 3 {
		t.Errorf("update %d, download %d, install %d times; want 3, 3 and 1\n%s", u, d, i, out)
	}
	if n := strings.Count(out, "::warning::"); n != 2 {
		t.Errorf("%d warnings for the two cut-off attempts\n%s", n, out)
	}

	// a mirror that is fine: one attempt, one install
	apt, logfile = fakeApt(t, 0)
	if out, err := runInstall(t, apt, "5"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if u, d, i := calls(t, logfile); u != 1 || d != 1 || i != 1 {
		t.Errorf("a fast mirror needs one update, one download and one install, got %d, %d, %d", u, d, i)
	}

	// a mirror that never delivers: the script gives up after its attempts and does NOT install a half set
	apt, logfile = fakeApt(t, 9)
	out, err = runInstall(t, apt, "3")
	if err == nil {
		t.Fatalf("it reported success although nothing was downloaded\n%s", out)
	}
	if u, d, i := calls(t, logfile); d != 3 || i != 0 || u != 3 {
		t.Errorf("update %d, download %d, install %d times; want 3, 3 and no install\n%s", u, d, i, out)
	}
	if !strings.Contains(out, "::error::could not download the packages in 3 attempts") {
		t.Errorf("no clear error message:\n%s", out)
	}
}
