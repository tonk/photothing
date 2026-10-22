// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The license is a promise to the people who get the program, so it is checked like code: the text is
// the real GPL version 3, every source file says so, and the packages carry the same identifier.
func TestLicense(t *testing.T) {
	text, err := os.ReadFile("LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GNU GENERAL PUBLIC LICENSE", "Version 3, 29 June 2007", "END OF TERMS AND CONDITIONS",
		"How to Apply These Terms to Your New Programs", "why-not-lgpl.html"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("LICENSE lacks %q: is it the complete GPL version 3?", want)
		}
	}
	if n := len(strings.Split(string(text), "\n")); n < 670 || n > 680 {
		t.Errorf("LICENSE has %d lines; the GPL version 3 has 674", n)
	}

	header := "SPDX-License-Identifier: " + license
	filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || (d.IsDir() && (d.Name() == ".git" || d.Name() == "dist")) {
			if err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !(strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".sh")) {
			return nil
		}
		b, _ := os.ReadFile(p)
		head := string(b)
		if len(head) > 300 {
			head = head[:300]
		}
		if !strings.Contains(head, header) || !strings.Contains(head, "Copyright (C) ") {
			t.Errorf("%s has no license header (%s)", p, header)
		}
		return nil
	})

	nfpm, _ := os.ReadFile("packaging/nfpm.yaml")
	if !strings.Contains(string(nfpm), "\nlicense: "+license+"\n") {
		t.Errorf("packaging/nfpm.yaml does not declare license %s", license)
	}
	if cr, _ := os.ReadFile("packaging/copyright"); !strings.Contains(string(cr), "License: "+license) {
		t.Errorf("packaging/copyright does not name %s", license)
	}
}

// Every Go module linked into the binary must bring a license file, and none may be a GPL-incompatible
// kind. A new dependency without a license makes this fail, and with it the release build.
func TestThirdPartyLicensesAreCollected(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	out := t.TempDir()
	if b, err := exec.Command("bash", "scripts/third-party-licenses.sh", out).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	index, err := os.ReadFile(filepath.Join(out, "INDEX.txt"))
	if err != nil || !strings.Contains(string(index), "golang.org/x/crypto") {
		t.Fatalf("index: %v\n%s", err, index)
	}
	filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		// Only the main license file of each module decides; the extra files that some modules ship (bundled
		// notices, the SQLite public-domain dedication, logo licenses) are informational.
		if err != nil || d.IsDir() || !(d.Name() == "LICENSE" || d.Name() == "LICENSE.txt" || d.Name() == "LICENSE.MIT") {
			return nil
		}
		b, _ := os.ReadFile(p)
		head := strings.ToLower(string(b))
		if len(head) > 3000 {
			head = head[:3000]
		}
		// GPL version 3 can use MIT, BSD, Apache-2.0 and public domain code; it cannot take (A)GPL-2.0-only,
		// proprietary or "no commercial use" licenses.
		for _, bad := range []string{"gnu affero", "gnu general public license", "non-commercial", "noncommercial", "all rights reserved. proprietary"} {
			if strings.Contains(head, bad) {
				t.Errorf("%s looks like a license that needs a decision (%q)", strings.TrimPrefix(p, out+"/"), bad)
			}
		}
		return nil
	})
}
