// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

// Package backup creates and restores archives of the data directory: a consistent snapshot of the
// SQLite database plus the secret key that decrypts the stored MFA seeds. The image cache is not
// included (it is rebuilt on demand), and neither are the photos themselves.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"photothing/internal/store"
)

const (
	DBName  = "photothing.db"
	KeyName = "secret.key"
	prefix  = "photothing-backup-"
	suffix  = ".tar.gz"

	maxDBSize = 8 << 30 // refuse absurd archives on restore
)

// DefaultName returns a timestamped file name for a new archive.
func DefaultName(now time.Time) string { return prefix + now.Format("20060102-150405") + suffix }

// Create writes a backup archive to out (which must not exist). The database is copied with
// VACUUM INTO, so this is safe while the server is running.
func Create(st *store.Store, dataDir, out string) error {
	tmp, err := os.MkdirTemp(dataDir, "backup-tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, DBName)
	if err := st.Backup(snap); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}

	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(out)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := addFile(tw, snap, DBName); err != nil {
		return err
	}
	if err := addFile(tw, filepath.Join(dataDir, KeyName), KeyName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err // a missing key just means MFA was never used
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func addFile(tw *tar.Writer, src, name string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.Copy(tw, in)
	return err
}

// Restore unpacks an archive into dataDir. It refuses to overwrite an existing database unless force
// is set; replaced files are kept next to the new ones with a ".before-restore" suffix. Do not
// restore while the server is running.
func Restore(archive, dataDir string, force bool) error {
	dbPath, keyPath := filepath.Join(dataDir, DBName), filepath.Join(dataDir, KeyName)
	if _, err := os.Stat(dbPath); err == nil && !force {
		return fmt.Errorf("%s already exists; use -force to replace it (the old files are kept)", dbPath)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}

	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("not a backup archive: %w", err)
	}
	defer gz.Close()

	tmp, err := os.MkdirTemp(dataDir, "restore-tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	got := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Only the two known files, as plain names: nothing in the archive can choose a path.
		if (h.Name != DBName && h.Name != KeyName) || h.Typeflag != tar.TypeReg {
			return fmt.Errorf("unexpected entry %q in archive", h.Name)
		}
		if h.Size > maxDBSize {
			return fmt.Errorf("entry %q is too large", h.Name)
		}
		out, err := os.OpenFile(filepath.Join(tmp, h.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, h.Size)); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		got[h.Name] = true
	}
	if !got[DBName] {
		return errors.New("archive contains no database")
	}

	// Open the restored copy: this validates it and upgrades its schema if the backup is older.
	st, err := store.Open(filepath.Join(tmp, DBName))
	if err != nil {
		return fmt.Errorf("restored database is unusable: %w", err)
	}
	if err := st.Check(); err != nil {
		st.Close()
		return fmt.Errorf("restored database is corrupt: %w", err)
	}
	st.Close() // flushes the WAL into the main file
	os.Remove(filepath.Join(tmp, DBName+"-wal"))
	os.Remove(filepath.Join(tmp, DBName+"-shm"))

	keep := func(p string) error {
		if _, err := os.Stat(p); err == nil {
			return os.Rename(p, p+".before-restore")
		}
		return nil
	}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := keep(p); err != nil {
			return err
		}
	}
	if got[KeyName] {
		if err := keep(keyPath); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(tmp, KeyName), keyPath); err != nil {
			return err
		}
	}
	return os.Rename(filepath.Join(tmp, DBName), dbPath)
}

// Prune keeps the newest `keep` archives in dir and deletes older ones that match the naming scheme.
func Prune(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamped names sort chronologically
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// Latest returns the time of the newest archive in dir (zero if none).
func Latest(dir string) time.Time {
	entries, _ := os.ReadDir(dir)
	var latest time.Time
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), suffix) {
			if info, err := e.Info(); err == nil && info.ModTime().After(latest) {
				latest = info.ModTime()
			}
		}
	}
	return latest
}
