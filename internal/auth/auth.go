// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

// Package auth holds password hashing, RBAC, TOTP and login throttling.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// ---- passwords ----

const MinPasswordLen = 12

func HashPassword(pw string) (string, error) {
	if len(pw) < MinPasswordLen {
		return "", fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), 12)

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// CheckDummy burns the same CPU time as a real check, so unknown users are not distinguishable by timing.
func CheckDummy(pw string) { bcrypt.CompareHashAndPassword(dummyHash, []byte(pw)) }

// ---- RBAC ----

type Perm string

const (
	PermView     Perm = "photo:view"
	PermOriginal Perm = "photo:original"
	PermEdit     Perm = "content:edit"
	PermScan     Perm = "library:scan"
	PermUsers    Perm = "users:manage"
)

var rolePerms = map[string][]Perm{
	"viewer": {PermView},
	"editor": {PermView, PermOriginal, PermEdit, PermScan},
	"admin":  {PermView, PermOriginal, PermEdit, PermScan, PermUsers},
}

func ValidRole(r string) bool { _, ok := rolePerms[r]; return ok }

func Can(role string, p Perm) bool {
	for _, q := range rolePerms[role] {
		if q == p {
			return true
		}
	}
	return false
}

// ---- tokens ----

// NewToken returns a random session token and the hash that is stored in the database.
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

func HashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// ---- secret box (encrypts TOTP seeds at rest) ----

type Box struct {
	aead cipher.AEAD
	mac  []byte // key for hashing recovery codes, derived from the box key
}

// LoadBox reads the key from path, creating it (mode 0600) on first use.
func LoadBox(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s: key must be 32 bytes", path)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("photothing recovery code hash key v1")) // a separate key for a separate purpose
	return &Box{aead: g, mac: m.Sum(nil)}, nil
}

// RecoveryHash is how a recovery code is stored: an HMAC under a key that lives in secret.key and not in
// the database. A recovery code has only about 50 bits, which is little against a copy of a database
// that was stolen without the key; with the key out of reach the stored values cannot be attacked at all.
func (b *Box) RecoveryHash(norm string) string {
	m := hmac.New(sha256.New, b.mac)
	m.Write([]byte(norm))
	return "v2:" + hex.EncodeToString(m.Sum(nil))
}

func (b *Box) Seal(plain string) string {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil))
}

func (b *Box) Open(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", errors.New("bad ciphertext")
	}
	n := b.aead.NonceSize()
	p, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	return string(p), err
}

// ---- TOTP ----

const totpPeriod = 30

func NewTOTP(issuer, account string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account, Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
}

// MatchTOTP checks code against the secret within ±1 step and returns the matching step.
// The caller must record the step to prevent replay.
func MatchTOTP(secret, code string, now time.Time) (step int64, ok bool) {
	if _, err := base32.StdEncoding.DecodeString(secret); err != nil {
		return 0, false
	}
	for off := -1; off <= 1; off++ {
		t := now.Add(time.Duration(off*totpPeriod) * time.Second)
		want, err := totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return t.Unix() / totpPeriod, true
		}
	}
	return 0, false
}

// ---- recovery codes ----

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no look-alikes (i, l, o, 0, 1)

// NewRecoveryCodes returns n codes formatted xxxxx-xxxxx and the values to store, made with hash
// (Box.RecoveryHash).
func NewRecoveryCodes(n int, hash func(string) string) (codes, hashes []string) {
	for i := 0; i < n; i++ {
		var b []byte
		for len(b) < 10 {
			buf := make([]byte, 16)
			if _, err := rand.Read(buf); err != nil {
				panic(err)
			}
			for _, c := range buf {
				if c < 248 && len(b) < 10 { // 248 = 8*31: no modulo bias
					b = append(b, recoveryAlphabet[int(c)%len(recoveryAlphabet)])
				}
			}
		}
		codes = append(codes, string(b[:5])+"-"+string(b[5:]))
		hashes = append(hashes, hash(string(b)))
	}
	return codes, hashes
}

// NormalizeRecovery lower-cases the input and drops spaces and dashes.
func NormalizeRecovery(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// LooksLikeRecovery reports whether a normalised input has the shape of a recovery code.
func LooksLikeRecovery(norm string) bool {
	if len(norm) != 10 {
		return false
	}
	for _, r := range norm {
		if !strings.ContainsRune(recoveryAlphabet, r) {
			return false
		}
	}
	return true
}

// HashRecovery is the unkeyed hash that versions before 0.1.0-rc stored; it is still accepted for codes
// that were issued then, and is never used for new ones.
func HashRecovery(norm string) string { return HashToken("recovery:" + norm) }

// ---- throttling ----

// Throttle is an in-memory failure counter: after max failures within window, the key is locked.
type Throttle struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func NewThrottle(max int, window time.Duration) *Throttle {
	return &Throttle{max: max, window: window, fails: map[string][]time.Time{}}
}

func (t *Throttle) prune(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, f := range t.fails[key] {
		if now.Sub(f) < t.window {
			keep = append(keep, f)
		}
	}
	if len(keep) == 0 {
		delete(t.fails, key)
	} else {
		t.fails[key] = keep
	}
	return keep
}

func (t *Throttle) Blocked(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.prune(key, time.Now())) >= t.max
}

// throttleMaxKeys bounds the memory of a Throttle: a flood of requests with ever-new keys (random user
// names) must not grow it without limit.
const throttleMaxKeys = 100_000

func (t *Throttle) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.fails)
}

func (t *Throttle) Fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if len(t.fails) >= throttleMaxKeys {
		for k := range t.fails { // forget what has expired
			t.prune(k, now)
		}
		if len(t.fails) >= throttleMaxKeys { // still full of live entries: start over rather than grow
			t.fails = map[string][]time.Time{}
		}
	}
	t.prune(key, now)
	t.fails[key] = append(t.fails[key], now)
}

func (t *Throttle) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, key)
}
