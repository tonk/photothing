// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"photothing/internal/store"
)

func setup(t *testing.T) (dataDir string, st *store.Store) {
	t.Helper()
	dataDir = t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, DBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := os.WriteFile(filepath.Join(dataDir, KeyName), []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir, st
}

func TestRoundTrip(t *testing.T) {
	dataDir, st := setup(t)
	if err := st.CreateUser("ton", "hash", "admin"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), DefaultName(time.Now()))
	if err := Create(st, dataDir, archive); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(archive); info.Mode().Perm() != 0o600 {
		t.Errorf("archive mode %v, want 0600", info.Mode().Perm())
	}
	// A second backup to the same name must not overwrite.
	if err := Create(st, dataDir, archive); err == nil {
		t.Error("existing archive was overwritten")
	}

	fresh := t.TempDir()
	if err := Restore(archive, fresh, false); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(filepath.Join(fresh, DBName))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if u, err := st2.UserByName("ton"); err != nil || u.Role != "admin" {
		t.Errorf("user not restored: %v %v", u, err)
	}
	if key, _ := os.ReadFile(filepath.Join(fresh, KeyName)); string(key) != "0123456789abcdef0123456789abcdef" {
		t.Error("key not restored")
	}

	// Restoring over an existing database needs -force and keeps the old one.
	if err := Restore(archive, fresh, false); err == nil {
		t.Error("restore over existing database without force")
	}
	st2.Close()
	if err := Restore(archive, fresh, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh, DBName+".before-restore")); err != nil {
		t.Error("old database was not kept")
	}
}

func TestRestoreRejectsHostileArchives(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../evil", "/etc/passwd", "sub/photothing.db", "other.txt"} {
		archive := filepath.Join(dir, "bad.tar.gz")
		f, _ := os.Create(archive)
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
		tw.Close()
		gz.Close()
		f.Close()
		target := t.TempDir()
		if err := Restore(archive, target, false); err == nil {
			t.Errorf("accepted entry %q", name)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("entry %q left files behind: %v", name, entries)
		}
		os.Remove(archive)
	}
	notArchive := filepath.Join(dir, "x.tar.gz")
	os.WriteFile(notArchive, []byte("plain text"), 0o600)
	if err := Restore(notArchive, t.TempDir(), false); err == nil {
		t.Error("accepted a non-archive")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(dir, DefaultName(base.AddDate(0, 0, i))), nil, 0o600)
	}
	os.WriteFile(filepath.Join(dir, "unrelated.tar.gz"), nil, 0o600)
	if err := Prune(dir, 2); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 { // 2 newest backups + the unrelated file
		t.Errorf("got %d files, want 3", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated.tar.gz")); err != nil {
		t.Error("pruned a file that is not a backup")
	}
}
