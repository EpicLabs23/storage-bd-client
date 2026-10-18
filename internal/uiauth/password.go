// Package uiauth holds the web UI's credential primitives: password
// hashing and checks. Shared by `storagebd user` (CLI) and `serve`.
package uiauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (RFC 9106's second recommended option, memory-
// constrained): 64 MiB, 3 passes. A sign-in costs ~50-100 ms of one core.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16

	MinPasswordLen = 12
	maxPasswordLen = 256
)

// ValidatePassword enforces a length floor rather than composition rules:
// length is what actually resists guessing.
func ValidatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	if n < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(p) > maxPasswordLen {
		return fmt.Errorf("password must be at most %d bytes", maxPasswordLen)
	}
	if strings.ContainsAny(p, "\r\n") {
		return errors.New("password must not contain line breaks")
	}
	return nil
}

// HashPassword returns a PHC-format argon2id string.
func HashPassword(p string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(p), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// burn spends one hash computation, so a missing user or a malformed
// stored hash takes as long as a wrong password.
func burn(p string) {
	argon2.IDKey([]byte(p), make([]byte, saltLen), argonTime, argonMemory, argonThreads, argonKeyLen)
}

// VerifyPassword reports whether p matches the PHC string. An empty or
// malformed hash never matches, but still costs one hash computation.
func VerifyPassword(p, phc string) bool {
	var v int
	var m, t uint32
	var par uint8
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		burn(p)
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		burn(p)
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &par); err != nil ||
		m == 0 || t == 0 || par == 0 || m > 1<<20 || t > 16 {
		burn(p)
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 64 {
		burn(p)
		return false
	}
	got := argon2.IDKey([]byte(p), salt, t, m, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
