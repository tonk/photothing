// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// semverRE is the regular expression from https://semver.org, without the leading "v".
const semverRE = `(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?`

func withBuild(t *testing.T, v, c string) {
	t.Helper()
	oldV, oldC := version, commit
	version, commit = v, c
	t.Cleanup(func() { version, commit = oldV, oldC })
}

func TestVersionFromLdflags(t *testing.T) {
	withBuild(t, "v1.2.3", "0123456789abcdef0123")
	got := versionString()
	if !strings.HasPrefix(got, "photothing 1.2.3 (commit 0123456789ab), go") {
		t.Errorf("got %q", got)
	}
	withBuild(t, "1.2.3-rc.1", "")
	if got := versionString(); !strings.HasPrefix(got, "photothing 1.2.3-rc.1, go") {
		t.Errorf("got %q", got)
	}
	withBuild(t, "1.0.0", "0123456789abcdef-dirty")
	if got := versionString(); !strings.Contains(got, "(commit 0123456789ab-dirty)") {
		t.Errorf("got %q", got)
	}
}

func TestVersionFallback(t *testing.T) {
	withBuild(t, "", "")
	v, _ := buildVersion()
	if !regexp.MustCompile(`^` + semverRE + `$`).MatchString(v) {
		t.Errorf("fallback version %q is not a semantic version", v)
	}
	if v != devVersion {
		t.Logf("version from the Go build information: %s", v)
	}
}

// CHANGELOG.md must be well-formed: every release is a valid semantic version, newest first, and the
// newest one is what the release process expects to publish.
func TestChangelog(t *testing.T) {
	b, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## [Unreleased]") {
		t.Error("CHANGELOG.md has no [Unreleased] section")
	}
	head := regexp.MustCompile(`(?m)^## \[(` + semverRE + `)\] - (\d{4}-\d{2}-\d{2})$`)
	all := regexp.MustCompile(`(?m)^## \[`).FindAllString(string(b), -1)
	found := head.FindAllStringSubmatch(string(b), -1)
	if len(found) == 0 {
		t.Fatal("no released version in CHANGELOG.md")
	}
	if len(all)-1 != len(found) { // everything but [Unreleased] must be a well-formed release heading
		t.Errorf("%d release headings, but only %d are \"## [X.Y.Z] - YYYY-MM-DD\"", len(all)-1, len(found))
	}
	seen := map[string]bool{}
	for _, m := range found {
		if seen[m[1]] {
			t.Errorf("version %s appears twice", m[1])
		}
		seen[m[1]] = true
	}
}

// The release scripts decide what may be published, so they are tested like code.
func TestReleaseScripts(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	run := func(script string, args ...string) (string, error) {
		out, err := exec.Command("bash", append([]string{script}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	b, _ := os.ReadFile("CHANGELOG.md")
	m := regexp.MustCompile(`(?m)^## \[(` + semverRE + `)\] - `).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("no release in CHANGELOG.md")
	}
	latest := m[1]

	if out, err := run("scripts/check-release.sh", "v"+latest); err != nil || out != latest {
		t.Errorf("the newest release v%s is refused: %q %v", latest, out, err)
	}
	for _, bad := range []string{latest, "v1.2", "v01.2.3", "v1.2.3+meta", "vX", "v" + latest + "-nosuchentry", "v99.99.99"} {
		if out, err := run("scripts/check-release.sh", bad); err == nil {
			t.Errorf("tag %q was accepted (%s)", bad, out)
		}
	}
	notes, err := run("scripts/release-notes.sh", latest)
	// the notes of a release are its entries under one or more of the Keep a Changelog headings, and nothing of
	// the next section
	if err != nil || !regexp.MustCompile(`(?m)^### (Added|Changed|Deprecated|Removed|Fixed|Security)$`).MatchString(notes) || strings.Contains(notes, "## [") {
		t.Errorf("release notes of %s: %v\n%s", latest, err, notes)
	}
	if _, err := run("scripts/release-notes.sh", "99.99.99"); err == nil {
		t.Error("release notes of a version that is not in the changelog")
	}
	// the version a build gets is overridable and never has a leading v
	if out, err := exec.Command("bash", "packaging/version.sh").CombinedOutput(); err != nil || !regexp.MustCompile(`^`+semverRE+`$`).MatchString(strings.TrimSpace(string(out))) {
		t.Errorf("packaging/version.sh printed %q (%v), which is not a semantic version", out, err)
	}
	cmd := exec.Command("bash", "packaging/version.sh")
	cmd.Env = append(os.Environ(), "VERSION=v2.0.0-rc.1")
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "2.0.0-rc.1" {
		t.Errorf("VERSION override gave %q", out)
	}
}

// The About box links to the source. Whatever a build does not override must be an address a visitor can open:
// no internal host, no ssh address.
func TestDefaultSourceAddress(t *testing.T) {
	if !regexp.MustCompile(`^https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`).MatchString(repoURL) {
		t.Errorf("default source address %q is not a public GitHub repository address", repoURL)
	}
	if strings.Contains(repoURL, ".git") || strings.HasSuffix(repoURL, "/") {
		t.Errorf("source address %q should be the page of the repository, without .git or a trailing slash", repoURL)
	}
}
