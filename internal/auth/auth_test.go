// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package auth

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestRBAC(t *testing.T) {
	cases := []struct {
		role string
		perm Perm
		want bool
	}{
		{"viewer", PermView, true}, {"viewer", PermOriginal, false}, {"viewer", PermEdit, false}, {"editor", PermEdit, true},
		{"editor", PermScan, true}, {"editor", PermUsers, false},
		{"admin", PermUsers, true}, {"nobody", PermView, false},
	}
	for _, c := range cases {
		if got := Can(c.role, c.perm); got != c.want {
			t.Errorf("Can(%s,%s)=%v want %v", c.role, c.perm, got, c.want)
		}
	}
}

func TestPasswordPolicyAndCheck(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Error("short password accepted")
	}
	h, err := HashPassword("a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "a-long-enough-password") || CheckPassword(h, "wrong") {
		t.Error("password check wrong")
	}
}

func TestTOTP(t *testing.T) {
	key, err := NewTOTP("test", "ton")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, _ := totp.GenerateCode(key.Secret(), now)
	step, ok := MatchTOTP(key.Secret(), code, now)
	if !ok || step != now.Unix()/30 {
		t.Errorf("valid code rejected (ok=%v step=%d)", ok, step)
	}
	if _, ok := MatchTOTP(key.Secret(), "000000x", now); ok {
		t.Error("garbage accepted")
	}
	old, _ := totp.GenerateCode(key.Secret(), now.Add(-5*time.Minute))
	if _, ok := MatchTOTP(key.Secret(), old, now); ok {
		t.Error("stale code accepted")
	}
}

func TestBox(t *testing.T) {
	b, err := LoadBox(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	enc := b.Seal("JBSWY3DPEHPK3PXP")
	if enc == "JBSWY3DPEHPK3PXP" {
		t.Error("not encrypted")
	}
	if got, err := b.Open(enc); err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Errorf("roundtrip: %q %v", got, err)
	}
	if _, err := b.Open(enc[:len(enc)-4] + "AAAA"); err == nil {
		t.Error("tampered ciphertext accepted")
	}
}

func TestThrottle(t *testing.T) {
	th := NewThrottle(3, time.Minute)
	for i := 0; i < 3; i++ {
		if th.Blocked("k") {
			t.Fatal("blocked too early")
		}
		th.Fail("k")
	}
	if !th.Blocked("k") || th.Blocked("other") {
		t.Error("throttle keying wrong")
	}
	th.Reset("k")
	if th.Blocked("k") {
		t.Error("reset failed")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes := NewRecoveryCodes(10)
	if len(codes) != 10 || len(hashes) != 10 {
		t.Fatal("wrong count")
	}
	seen := map[string]bool{}
	for i, c := range codes {
		if len(c) != 11 || c[5] != '-' {
			t.Errorf("bad format %q", c)
		}
		n := NormalizeRecovery(strings.ToUpper(c))
		if !LooksLikeRecovery(n) || HashRecovery(n) != hashes[i] {
			t.Errorf("code %q does not round-trip", c)
		}
		if seen[c] {
			t.Error("duplicate code")
		}
		seen[c] = true
	}
	if LooksLikeRecovery("123456") || LooksLikeRecovery("abcde-fghjk") {
		t.Error("TOTP code or unnormalised input treated as recovery code")
	}
}
