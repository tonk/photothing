// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// oldSchema is the database layout of the first released version (before languages, page sizes,
// sessions metadata, captions and video).
const oldSchema = `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL,
	role TEXT NOT NULL, totp_secret TEXT NOT NULL DEFAULT '', totp_enabled INTEGER NOT NULL DEFAULT 0,
	totp_last_step INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
CREATE TABLE sessions (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	mfa_pending INTEGER NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE photos (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, dir TEXT NOT NULL, filename TEXT NOT NULL,
	width INTEGER NOT NULL, height INTEGER NOT NULL, taken_at INTEGER NOT NULL, size INTEGER NOT NULL,
	mtime INTEGER NOT NULL, exif TEXT NOT NULL DEFAULT '{}');
CREATE TABLE album_covers (dir TEXT PRIMARY KEY, photo_id INTEGER NOT NULL REFERENCES photos(id) ON DELETE CASCADE);
CREATE INDEX photos_dir ON photos(dir, taken_at, filename);
CREATE INDEX photos_dir_name ON photos(dir, filename COLLATE NOCASE);
INSERT INTO users(id, username, password_hash, role, created_at) VALUES (1, 'ton', 'hash', 'admin', 1);
INSERT INTO photos(id, path, dir, filename, width, height, taken_at, size, mtime) VALUES (7, 'a/x.jpg', 'a', 'x.jpg', 10, 20, 5, 100, 6);
INSERT INTO album_covers(dir, photo_id) VALUES ('a', 7);
INSERT INTO sessions(token_hash, user_id, mfa_pending, expires_at) VALUES ('abc', 1, 0, 4102444800);
INSERT INTO settings(key, value) VALUES ('public', '1');
`

func TestUpgradeFromEarlierVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	defer st.Close()

	u, err := st.UserByName("ton")
	if err != nil || u.Role != "admin" || u.Language != "" || u.PageSize != 0 || u.Sort != "" {
		t.Errorf("user after upgrade: %+v %v", u, err)
	}
	p, err := st.PhotoByID(7)
	if err != nil || p.Kind != "image" || p.Caption != "" || p.Width != 10 {
		t.Errorf("photo after upgrade: %+v %v", p, err)
	}
	covers, _ := st.Covers()
	if covers["a"].PhotoID != 7 || covers["a"].PhotoDir != "a" {
		t.Errorf("covers after upgrade: %v", covers)
	}
	if se, err := st.SessionByHash("abc"); err != nil || se.IP != "" {
		t.Errorf("session after upgrade: %+v %v", se, err)
	}
	if st.Setting("public") != "1" {
		t.Error("settings lost")
	}
	// all new features work on the upgraded database
	if err := st.SetPrefs(1, "nl", 24, "natural"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCaption(7, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTags(7, []string{"b", "a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddAudit(AuditEntry{Actor: "ton", Action: "x"}); err != nil {
		t.Fatal(err)
	}
	if refs, err := st.Search("hello", ""); err != nil || len(refs) != 1 {
		t.Errorf("search: %v %v", refs, err)
	}
	// opening again is a no-op
	st.Close()
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	st2.Close()
}

func TestSubAlbumsRespectsAccessAndCovers(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []Photo{
		{Path: "v/b1/Zeta.jpg", Dir: "v/b1", Filename: "Zeta.jpg"},
		{Path: "v/b1/alpha.jpg", Dir: "v/b1", Filename: "alpha.jpg"},
		{Path: "v/b2/c.jpg", Dir: "v/b2", Filename: "c.jpg"},
		{Path: "v/secret/s.jpg", Dir: "v/secret", Filename: "s.jpg"},
	} {
		p.Exif = map[string]string{}
		if err := st.UpsertPhoto(&p); err != nil {
			t.Fatal(err)
		}
	}
	all := func(string) bool { return true }
	subs, _ := st.SubAlbums("", all)
	if len(subs) != 1 || subs[0].Name != "v" || subs[0].Count != 4 {
		t.Fatalf("root: %+v", subs)
	}
	alpha, _ := st.PhotoIndex()
	if subs[0].Cover != alpha["v/b1/alpha.jpg"].ID {
		t.Errorf("automatic cover should be the alphabetically first photo of the first directory, got id %d", subs[0].Cover)
	}
	noSecret := func(d string) bool { return d != "v/secret" }
	if subs, _ = st.SubAlbums("v", noSecret); len(subs) != 2 {
		t.Errorf("restricted directory still listed: %+v", subs)
	}
	secretID := alpha["v/secret/s.jpg"].ID
	st.SetCover("v", secretID)
	if subs, _ = st.SubAlbums("", noSecret); subs[0].Cover == secretID {
		t.Error("explicit cover in a hidden directory was used")
	}
	if subs, _ = st.SubAlbums("", all); subs[0].Cover != secretID {
		t.Error("explicit cover ignored for a visitor who may see it")
	}
}
