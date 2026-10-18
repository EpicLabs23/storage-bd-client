package uiauth

import (
	"strings"
	"testing"
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
