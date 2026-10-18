package uiauth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash format: %s", h)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("same salt twice")
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Fatal("right password rejected")
	}
	for _, p := range []string{"correct horse batterY", "", "correct horse battery "} {
		if VerifyPassword(p, h) {
			t.Fatalf("%q accepted", p)
		}
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA", "$argon2id$v=19$m=0,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=99999999,t=3,p=2$c2FsdA$aGFzaA"} {
		if VerifyPassword("x", bad) {
			t.Fatalf("malformed hash %q matched", bad)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if ValidatePassword("short") == nil || ValidatePassword("twelve chars\nx") == nil || ValidatePassword(strings.Repeat("a", 300)) == nil {
		t.Fatal("bad password accepted")
	}
	if err := ValidatePassword("twelve chars"); err != nil {
		t.Fatal(err)
	}
}

func TestTOTPRFC6238(t *testing.T) {
	// RFC 6238 appendix B, SHA-1 key "12345678901234567890"; last 6 digits.
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		now := time.Unix(tc.unix, 0)
		if got := TOTPCode(secret, now); got != tc.code {
			t.Fatalf("t=%d: %s, want %s", tc.unix, got, tc.code)
		}
		step, ok := CheckTOTP(secret, tc.code, now, 0)
		if !ok || step != tc.unix/30 {
			t.Fatalf("t=%d not accepted", tc.unix)
		}
		// One step of drift either way is fine, two isn't.
		if _, ok := CheckTOTP(secret, tc.code, now.Add(30*time.Second), 0); !ok {
			t.Fatal("+1 step refused")
		}
		if _, ok := CheckTOTP(secret, tc.code, now.Add(61*time.Second), 0); ok {
			t.Fatal("+2 steps accepted")
		}
		// Replay: the same step can't be used again.
		if _, ok := CheckTOTP(secret, tc.code, now, step); ok {
			t.Fatal("used step accepted again")
		}
	}
	if _, ok := CheckTOTP(secret, "12345", time.Now(), 0); ok {
		t.Fatal("short code accepted")
	}
}

func TestBackupCodesAndQR(t *testing.T) {
	codes, hashes, err := NewBackupCodes()
	if err != nil || len(codes) != BackupCodeCount || len(hashes) != BackupCodeCount {
		t.Fatal(codes, err)
	}
	if MatchBackupCode(strings.ToUpper(strings.ReplaceAll(codes[3], "-", " ")), hashes) != 3 {
		t.Fatal("code not matched loosely")
	}
	if MatchBackupCode("nope-nope-nope", hashes) != -1 || LooksLikeTOTP(codes[0]) || !LooksLikeTOTP("012345") {
		t.Fatal("matching")
	}
	uri := TOTPURI("admin@host", "JBSWY3DPEHPK3PXP")
	if !strings.HasPrefix(uri, "otpauth://totp/storage.bd%20backup:admin@host?") || !strings.Contains(uri, "secret=JBSWY3DPEHPK3PXP") {
		t.Fatal(uri)
	}
	svg, err := QRSVG(uri)
	if err != nil || !strings.HasPrefix(svg, "<svg") {
		t.Fatal(err)
	}
	if _, err := QRTerminal(uri); err != nil {
		t.Fatal(err)
	}
}
