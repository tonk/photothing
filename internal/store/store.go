// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

// Package store wraps the SQLite database.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// schema is the full current schema for fresh databases. Columns added later are also listed in
// migrate(), which upgrades databases created by earlier versions.
const schema = `
CREATE TABLE IF NOT EXISTS users (
	id             INTEGER PRIMARY KEY,
	username       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash  TEXT NOT NULL,
	role           TEXT NOT NULL,
	totp_secret    TEXT NOT NULL DEFAULT '',
	totp_enabled   INTEGER NOT NULL DEFAULT 0,
	totp_last_step INTEGER NOT NULL DEFAULT 0,
	disabled       INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL,
	language       TEXT NOT NULL DEFAULT '',
	page_size      INTEGER NOT NULL DEFAULT 0,
	sort           TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash  TEXT PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	mfa_pending INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	created_at  INTEGER NOT NULL DEFAULT 0,
	ip          TEXT NOT NULL DEFAULT '',
	user_agent  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS photos (
	id       INTEGER PRIMARY KEY,
	path     TEXT NOT NULL UNIQUE,
	dir      TEXT NOT NULL,
	filename TEXT NOT NULL,
	width    INTEGER NOT NULL,
	height   INTEGER NOT NULL,
	taken_at INTEGER NOT NULL,
	size     INTEGER NOT NULL,
	mtime    INTEGER NOT NULL,
	exif     TEXT NOT NULL DEFAULT '{}',
	kind     TEXT NOT NULL DEFAULT 'image',
	caption  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS album_covers (
	dir      TEXT PRIMARY KEY,
	photo_id INTEGER NOT NULL REFERENCES photos(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS tags (
	photo_id INTEGER NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
	tag      TEXT NOT NULL COLLATE NOCASE,
	PRIMARY KEY (photo_id, tag)
);
CREATE INDEX IF NOT EXISTS tags_tag ON tags(tag);
CREATE TABLE IF NOT EXISTS recovery_codes (
	user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	code_hash TEXT NOT NULL,
	used_at   INTEGER,
	PRIMARY KEY (user_id, code_hash)
);
CREATE TABLE IF NOT EXISTS password_resets (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	id     INTEGER PRIMARY KEY,
	ts     INTEGER NOT NULL,
	actor  TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	ip     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts);
CREATE TABLE IF NOT EXISTS album_rules (
	dir TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS album_users (
	dir     TEXT NOT NULL REFERENCES album_rules(dir) ON DELETE CASCADE,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	PRIMARY KEY (dir, user_id)
);
DROP INDEX IF EXISTS photos_dir;
DROP INDEX IF EXISTS photos_dir_name;
CREATE INDEX IF NOT EXISTS photos_dir_idx ON photos(dir);
`

// migrations bring databases from earlier versions up to the current schema.
var migrations = []struct{ table, column, def string }{
	{"users", "language", "TEXT NOT NULL DEFAULT ''"},
	{"users", "page_size", "INTEGER NOT NULL DEFAULT 0"},
	{"users", "sort", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "created_at", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "ip", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "user_agent", "TEXT NOT NULL DEFAULT ''"},
	{"photos", "kind", "TEXT NOT NULL DEFAULT 'image'"},
	{"photos", "caption", "TEXT NOT NULL DEFAULT ''"},
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	// Add missing columns first: the schema below may create indexes that use them.
	for _, m := range migrations {
		var exists int
		s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, m.table).Scan(&exists)
		if exists == 0 {
			continue // fresh database: the schema below creates the table with all columns
		}
		has, err := s.hasColumn(m.table, m.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.Exec(`ALTER TABLE ` + m.table + ` ADD COLUMN ` + m.column + ` ` + m.def); err != nil {
				return err
			}
		}
	}
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if n == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// Backup writes a consistent snapshot of the database to path (which must not exist).
func (s *Store) Backup(path string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

// Check runs SQLite's integrity check.
func (s *Store) Check() error {
	var res string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return errors.New(res)
	}
	return nil
}

// ---- users ----

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	TOTPSecret   string // encrypted by the auth package
	TOTPEnabled  bool
	TOTPLastStep int64
	Disabled     bool
	Language     string // "" = not chosen
	PageSize     int    // 0 = default
	Sort         string // "" = default
}

const userCols = `id, username, password_hash, role, totp_secret, totp_enabled, totp_last_step, disabled, language, page_size, sort`

func scanUser(r interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &u.TOTPEnabled, &u.TOTPLastStep, &u.Disabled, &u.Language, &u.PageSize, &u.Sort)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) CreateUser(username, hash, role string) error {
	_, err := s.db.Exec(`INSERT INTO users(username, password_hash, role, created_at) VALUES(?,?,?,?)`,
		username, hash, role, time.Now().Unix())
	return err
}

func (s *Store) UserByName(name string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, name))
}

func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) SetRole(id int64, role string) error {
	_, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	return err
}

// SetPrefs stores the user's interface preferences.
func (s *Store) SetPrefs(id int64, language string, pageSize int, sort string) error {
	_, err := s.db.Exec(`UPDATE users SET language = ?, page_size = ?, sort = ? WHERE id = ?`, language, pageSize, sort, id)
	return err
}

// SetDisabled blocks or unblocks a user; blocking also revokes their sessions.
func (s *Store) SetDisabled(id int64, disabled bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET disabled = ? WHERE id = ?`, disabled, id); err != nil {
		return err
	}
	if disabled {
		if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteUser removes the user; their sessions, codes and access entries go with them (ON DELETE CASCADE).
func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// SetPassword changes the password and revokes all of the user's sessions and reset links.
func (s *Store) SetPassword(id int64, hash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM password_resets WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetTOTP stores the (encrypted) secret. Turning MFA off also deletes the recovery codes.
func (s *Store) SetTOTP(id int64, encSecret string, enabled bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET totp_secret = ?, totp_enabled = ?, totp_last_step = 0 WHERE id = ?`, encSecret, enabled, id); err != nil {
		return err
	}
	if !enabled {
		if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ConsumeTOTPStep records a used time step; it fails if the step was already used (replay).
func (s *Store) ConsumeTOTPStep(id, step int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ---- recovery codes ----

// ReplaceRecoveryCodes deletes the user's codes and stores the new hashes.
func (s *Store) ReplaceRecoveryCodes(userID int64, hashes []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(`INSERT INTO recovery_codes(user_id, code_hash) VALUES(?,?)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UseRecoveryCode marks a code as used; it reports false for unknown or already used codes.
func (s *Store) UseRecoveryCode(userID int64, hash string) (bool, error) {
	res, err := s.db.Exec(`UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		time.Now().Unix(), userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) CountRecoveryCodes(userID int64) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	return n
}

// ---- sessions ----

type Session struct {
	TokenHash  string
	UserID     int64
	MFAPending bool
	ExpiresAt  time.Time
	CreatedAt  time.Time
	IP         string
	UserAgent  string
}

func (s *Store) CreateSession(tokenHash string, userID int64, mfaPending bool, expires time.Time, ip, userAgent string) error {
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash, user_id, mfa_pending, expires_at, created_at, ip, user_agent) VALUES(?,?,?,?,?,?,?)`,
		tokenHash, userID, mfaPending, expires.Unix(), time.Now().Unix(), ip, userAgent)
	return err
}

func (s *Store) SessionByHash(hash string) (*Session, error) {
	var se Session
	var exp, created int64
	err := s.db.QueryRow(`SELECT token_hash, user_id, mfa_pending, expires_at, created_at, ip, user_agent FROM sessions WHERE token_hash = ?`, hash).
		Scan(&se.TokenHash, &se.UserID, &se.MFAPending, &exp, &created, &se.IP, &se.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	se.ExpiresAt, se.CreatedAt = time.Unix(exp, 0), time.Unix(created, 0)
	if time.Now().After(se.ExpiresAt) {
		s.DeleteSession(hash)
		return nil, ErrNotFound
	}
	return &se, nil
}

// ListSessions returns the user's fully signed-in sessions, newest first.
func (s *Store) ListSessions(userID int64) ([]Session, error) {
	rows, err := s.db.Query(`SELECT token_hash, user_id, expires_at, created_at, ip, user_agent FROM sessions
		WHERE user_id = ? AND mfa_pending = 0 AND expires_at > ? ORDER BY created_at DESC`, userID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var se Session
		var exp, created int64
		if err := rows.Scan(&se.TokenHash, &se.UserID, &exp, &created, &se.IP, &se.UserAgent); err != nil {
			return nil, err
		}
		se.ExpiresAt, se.CreatedAt = time.Unix(exp, 0), time.Unix(created, 0)
		out = append(out, se)
	}
	return out, rows.Err()
}

// DeleteSessionByPrefix revokes one of the user's sessions, identified by a hex prefix of its hash.
func (s *Store) DeleteSessionByPrefix(userID int64, prefix string) (bool, error) {
	if len(prefix) < 12 {
		return false, nil
	}
	res, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND substr(token_hash, 1, ?) = ?`, userID, len(prefix), prefix)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) DeleteOtherSessions(userID int64, keepHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, keepHash)
	return err
}

func (s *Store) DeleteSession(hash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}

func (s *Store) DeleteUserSessions(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// Purge removes expired sessions and reset links and audit entries older than auditKeep.
func (s *Store) Purge(auditKeep time.Duration) error {
	now := time.Now().Unix()
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM password_resets WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM audit WHERE ts < ?`, time.Now().Add(-auditKeep).Unix())
	return err
}

// ---- password reset links ----

func (s *Store) CreateReset(tokenHash string, userID int64, expires time.Time) error {
	_, err := s.db.Exec(`INSERT INTO password_resets(token_hash, user_id, expires_at) VALUES(?,?,?)`, tokenHash, userID, expires.Unix())
	return err
}

// ResetUser returns the user a valid (unexpired) reset link belongs to.
func (s *Store) ResetUser(tokenHash string) (*User, error) {
	var id int64
	err := s.db.QueryRow(`SELECT user_id FROM password_resets WHERE token_hash = ? AND expires_at > ?`, tokenHash, time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u, err := s.UserByID(id)
	if err == nil && u.Disabled {
		return nil, ErrNotFound
	}
	return u, err
}

// ---- audit log ----

type AuditEntry struct {
	ID     int64
	Time   time.Time
	Actor  string
	Action string
	Target string
	Detail string
	IP     string
}

// Failed and blocked sign-ins can be produced by anybody, as often as they like. They are kept apart from
// the entries that record what administrators and users did, and only the newest auditNoiseKeep of them
// are kept, so a flood can neither fill the disk nor push the real history out of the log.
const auditNoiseKeep = 20_000

const noisyActions = `'login.fail','login.blocked','mfa.fail','mfa.blocked'`

func (s *Store) AddAudit(e AuditEntry) error {
	for _, f := range []*string{&e.Actor, &e.Target, &e.Detail, &e.IP} { // nothing unbounded goes into the log
		if r := []rune(*f); len(r) > 200 {
			*f = string(r[:200])
		}
	}
	res, err := s.db.Exec(`INSERT INTO audit(ts, actor, action, target, detail, ip) VALUES(?,?,?,?,?,?)`,
		time.Now().Unix(), e.Actor, e.Action, e.Target, e.Detail, e.IP)
	if err != nil {
		return err
	}
	// Trim every so often rather than on each insert: one id in a hundred triggers the clean-up.
	if id, _ := res.LastInsertId(); id%100 == 0 && strings.Contains(noisyActions, "'"+e.Action+"'") {
		_, err = s.db.Exec(`DELETE FROM audit WHERE action IN (`+noisyActions+`) AND id <= COALESCE(
			(SELECT id FROM audit WHERE action IN (`+noisyActions+`) ORDER BY id DESC LIMIT 1 OFFSET ?), 0)`, auditNoiseKeep)
	}
	return err
}

func (s *Store) ListAudit(limit, offset int) ([]AuditEntry, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT id, ts, actor, action, target, detail, ip FROM audit ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.IP); err != nil {
			return nil, 0, err
		}
		e.Time = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// ---- settings ----

func (s *Store) Setting(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- photos ----

type Photo struct {
	ID       int64
	Path     string // slash separated, relative to the photo root
	Dir      string // "" for the root
	Filename string
	Width    int
	Height   int
	TakenAt  time.Time
	Size     int64
	MTime    int64
	Exif     map[string]string
	Kind     string // "image" or "video"
	Caption  string
}

func (p *Photo) IsVideo() bool { return p.Kind == "video" }

const photoCols = `id, path, dir, filename, width, height, taken_at, size, mtime, exif, kind, caption`

func scanPhoto(r interface{ Scan(...any) error }) (*Photo, error) {
	var p Photo
	var taken int64
	var exif string
	err := r.Scan(&p.ID, &p.Path, &p.Dir, &p.Filename, &p.Width, &p.Height, &taken, &p.Size, &p.MTime, &exif, &p.Kind, &p.Caption)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.TakenAt = time.Unix(taken, 0)
	p.Exif = map[string]string{}
	json.Unmarshal([]byte(exif), &p.Exif)
	return &p, nil
}

func (s *Store) PhotoByID(id int64) (*Photo, error) {
	return scanPhoto(s.db.QueryRow(`SELECT `+photoCols+` FROM photos WHERE id = ?`, id))
}

// PhotosByIDs returns the photos in the order of ids; unknown ids are skipped.
func (s *Store) PhotosByIDs(ids []int64) ([]Photo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id
	}
	rows, err := s.db.Query(`SELECT `+photoCols+` FROM photos WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]Photo{}
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		byID[p.ID] = *p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Photo, 0, len(ids))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// PhotoRef is the light-weight form of a photo used for listing, sorting and paging.
type PhotoRef struct {
	ID       int64
	Dir      string
	Filename string
	TakenAt  time.Time
}

func scanRefs(rows *sql.Rows) ([]PhotoRef, error) {
	defer rows.Close()
	var out []PhotoRef
	for rows.Next() {
		var r PhotoRef
		var t int64
		if err := rows.Scan(&r.ID, &r.Dir, &r.Filename, &t); err != nil {
			return nil, err
		}
		r.TakenAt = time.Unix(t, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DirRefs lists the photos directly in dir (unsorted).
func (s *Store) DirRefs(dir string) ([]PhotoRef, error) {
	rows, err := s.db.Query(`SELECT id, dir, filename, taken_at FROM photos WHERE dir = ?`, dir)
	if err != nil {
		return nil, err
	}
	return scanRefs(rows)
}

func likeWord(w string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(w) + "%"
}

// Search finds photos where every word of q matches the filename, caption, album path or a tag,
// optionally restricted to an exact tag.
func (s *Store) Search(q, tag string) ([]PhotoRef, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT id, dir, filename, taken_at FROM photos p WHERE 1=1`)
	var args []any
	words := strings.Fields(q)
	if len(words) > 8 {
		words = words[:8]
	}
	for _, w := range words {
		if len(w) > 64 {
			w = w[:64]
		}
		l := likeWord(w)
		sb.WriteString(` AND (p.filename LIKE ? ESCAPE '\' OR p.caption LIKE ? ESCAPE '\' OR p.dir LIKE ? ESCAPE '\'
			OR EXISTS (SELECT 1 FROM tags t WHERE t.photo_id = p.id AND t.tag LIKE ? ESCAPE '\'))`)
		args = append(args, l, l, l, l)
	}
	if tag != "" {
		sb.WriteString(` AND EXISTS (SELECT 1 FROM tags t WHERE t.photo_id = p.id AND t.tag = ?)`)
		args = append(args, tag)
	}
	rows, err := s.db.Query(sb.String(), args...)
	if err != nil {
		return nil, err
	}
	return scanRefs(rows)
}

type IndexEntry struct {
	ID    int64
	MTime int64
	Size  int64
}

func (s *Store) PhotoIndex() (map[string]IndexEntry, error) {
	rows, err := s.db.Query(`SELECT path, id, mtime, size FROM photos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]IndexEntry{}
	for rows.Next() {
		var p string
		var e IndexEntry
		if err := rows.Scan(&p, &e.ID, &e.MTime, &e.Size); err != nil {
			return nil, err
		}
		out[p] = e
	}
	return out, rows.Err()
}

func (s *Store) UpsertPhoto(p *Photo) error {
	exif, _ := json.Marshal(p.Exif)
	kind := p.Kind
	if kind == "" {
		kind = "image"
	}
	_, err := s.db.Exec(`INSERT INTO photos(path, dir, filename, width, height, taken_at, size, mtime, exif, kind)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET width=excluded.width, height=excluded.height, taken_at=excluded.taken_at,
			size=excluded.size, mtime=excluded.mtime, exif=excluded.exif, kind=excluded.kind`,
		p.Path, p.Dir, p.Filename, p.Width, p.Height, p.TakenAt.Unix(), p.Size, p.MTime, string(exif), kind)
	return err
}

func (s *Store) DeletePhoto(id int64) error {
	_, err := s.db.Exec(`DELETE FROM photos WHERE id = ?`, id)
	return err
}

// MovePhoto records that a file was renamed or moved, keeping its id, caption, tags and covers.
// Album covers that pointed at the photo follow a rename of its directory (same depth); otherwise
// covers of albums the photo has left are dropped.
func (s *Store) MovePhoto(id int64, newPath, newDir, newName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldDir string
	if err := tx.QueryRow(`SELECT dir FROM photos WHERE id = ?`, id).Scan(&oldDir); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE photos SET path = ?, dir = ?, filename = ? WHERE id = ?`, newPath, newDir, newName, id); err != nil {
		return err
	}
	if oldDir != newDir {
		rows, err := tx.Query(`SELECT dir FROM album_covers WHERE photo_id = ?`, id)
		if err != nil {
			return err
		}
		var covers []string
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				return err
			}
			covers = append(covers, d)
		}
		rows.Close()
		oldParts, newParts := strings.Split(oldDir, "/"), strings.Split(newDir, "/")
		for _, c := range covers {
			if newDir == c || strings.HasPrefix(newDir, c+"/") {
				continue // still inside that album
			}
			k := len(strings.Split(c, "/"))
			keep := ""
			if len(oldParts) == len(newParts) && k <= len(newParts) {
				keep = strings.Join(newParts[:k], "/")
			}
			if _, err := tx.Exec(`DELETE FROM album_covers WHERE dir = ?`, c); err != nil {
				return err
			}
			if keep != "" {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO album_covers(dir, photo_id) VALUES(?,?)`, keep, id); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

// ---- captions and tags ----

func (s *Store) SetCaption(id int64, caption string) error {
	_, err := s.db.Exec(`UPDATE photos SET caption = ? WHERE id = ?`, caption, id)
	return err
}

func (s *Store) PhotoTags(id int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT tag FROM tags WHERE photo_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTags replaces the tags of a photo.
func (s *Store) SetTags(id int64, tags []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tags WHERE photo_id = ?`, id); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO tags(photo_id, tag) VALUES(?,?)`, id, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type TagCount struct {
	Tag   string
	Count int
}

// TagCounts counts photos per tag, only in directories for which allow returns true.
func (s *Store) TagCounts(allow func(dir string) bool) ([]TagCount, error) {
	rows, err := s.db.Query(`SELECT t.tag, p.dir, COUNT(*) FROM tags t JOIN photos p ON p.id = t.photo_id GROUP BY t.tag, p.dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var tag, dir string
		var n int
		if err := rows.Scan(&tag, &dir, &n); err != nil {
			return nil, err
		}
		if allow(dir) {
			counts[strings.ToLower(tag)] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]TagCount, 0, len(counts))
	for t, n := range counts {
		out = append(out, TagCount{t, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out, nil
}

// ---- albums ----

type SubAlbum struct {
	Name  string
	Path  string
	Cover int64
	Count int
}

type DirStat struct {
	Dir     string
	Count   int
	FirstID int64  // alphabetically first photo in the directory
	First   string // its file name
}

// DirStats returns one row per directory that directly contains photos (not the root).
func (s *Store) DirStats() ([]DirStat, error) {
	// SQLite returns the bare column (id) of the row that holds the MIN.
	rows, err := s.db.Query(`SELECT dir, COUNT(*), id, MIN(lower(filename)) FROM photos WHERE dir != '' GROUP BY dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirStat
	for rows.Next() {
		var d DirStat
		if err := rows.Scan(&d.Dir, &d.Count, &d.FirstID, &d.First); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SubAlbums lists the immediate child directories of dir that contain photos (directly or deeper),
// counting only directories for which allow returns true. The cover is the one chosen with SetCover
// (if its directory is allowed), else the alphabetically first photo of the first allowed directory.
func (s *Store) SubAlbums(dir string, allow func(dir string) bool) ([]SubAlbum, error) {
	stats, err := s.DirStats()
	if err != nil {
		return nil, err
	}
	covers, err := s.Covers()
	if err != nil {
		return nil, err
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	type cand struct{ dir, file string }
	byName := map[string]*SubAlbum{}
	best := map[string]cand{}
	for _, d := range stats {
		if !strings.HasPrefix(d.Dir, prefix) || !allow(d.Dir) {
			continue
		}
		name, _, _ := strings.Cut(d.Dir[len(prefix):], "/")
		c := cand{strings.ToLower(d.Dir), d.First}
		a := byName[name]
		if a == nil {
			a = &SubAlbum{Name: name, Path: prefix + name, Cover: d.FirstID}
			byName[name] = a
			best[name] = c
		} else if b := best[name]; c.dir < b.dir || (c.dir == b.dir && c.file < b.file) {
			a.Cover, best[name] = d.FirstID, c
		}
		a.Count += d.Count
	}
	out := make([]SubAlbum, 0, len(byName))
	for _, a := range byName {
		if c, ok := covers[a.Path]; ok && allow(c.PhotoDir) {
			a.Cover = c.PhotoID
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

type Cover struct {
	PhotoID  int64
	PhotoDir string
}

// Covers returns the explicitly chosen cover per album directory.
func (s *Store) Covers() (map[string]Cover, error) {
	rows, err := s.db.Query(`SELECT c.dir, c.photo_id, p.dir FROM album_covers c JOIN photos p ON p.id = c.photo_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Cover{}
	for rows.Next() {
		var d string
		var c Cover
		if err := rows.Scan(&d, &c.PhotoID, &c.PhotoDir); err != nil {
			return nil, err
		}
		out[d] = c
	}
	return out, rows.Err()
}

// SetCover makes the photo the cover of album dir. The photo must live in dir or below it.
func (s *Store) SetCover(dir string, photoID int64) error {
	p, err := s.PhotoByID(photoID)
	if err != nil {
		return err
	}
	if dir == "" || (p.Dir != dir && !strings.HasPrefix(p.Dir, dir+"/")) {
		return errors.New("photo is not part of that album")
	}
	_, err = s.db.Exec(`INSERT INTO album_covers(dir, photo_id) VALUES(?,?)
		ON CONFLICT(dir) DO UPDATE SET photo_id = excluded.photo_id`, dir, photoID)
	return err
}

// ResetCover goes back to the automatic cover.
func (s *Store) ResetCover(dir string) error {
	_, err := s.db.Exec(`DELETE FROM album_covers WHERE dir = ?`, dir)
	return err
}

// ---- album access rules ----

// AccessRules returns, per restricted album directory, the set of users allowed in (admins always are).
func (s *Store) AccessRules() (map[string]map[int64]bool, error) {
	out := map[string]map[int64]bool{}
	rows, err := s.db.Query(`SELECT dir FROM album_rules`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		out[d] = map[int64]bool{}
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT dir, user_id FROM album_users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var id int64
		if err := rows.Scan(&d, &id); err != nil {
			return nil, err
		}
		if m, ok := out[d]; ok {
			m[id] = true
		}
	}
	return out, rows.Err()
}

// SetAccess restricts album dir to the given users (plus admins), replacing any earlier list.
func (s *Store) SetAccess(dir string, userIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO album_rules(dir) VALUES(?)`, dir); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM album_users WHERE dir = ?`, dir); err != nil {
		return err
	}
	for _, id := range userIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO album_users(dir, user_id) VALUES(?,?)`, dir, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteAccess removes the restriction on dir.
func (s *Store) DeleteAccess(dir string) error {
	_, err := s.db.Exec(`DELETE FROM album_rules WHERE dir = ?`, dir)
	return err
}
