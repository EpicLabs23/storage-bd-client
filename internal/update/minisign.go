package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// minisign formats (https://jedisct1.github.io/minisign/):
//
//	public key, line 2:  base64("Ed" || key id [8] || Ed25519 public key [32])
//	signature,  line 2:  base64("ED" || key id [8] || Ed25519(BLAKE2b-512(file)) [64])
//	            line 3:  "trusted comment: " + text
//	            line 4:  base64(Ed25519(signature [64] || text))
//
// Only the prehashed "ED" algorithm (minisign's default since 0.10) is
// accepted; the legacy unhashed "Ed" signatures are refused.

type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// IDString is the key id as minisign prints it.
func (k PublicKey) IDString() string {
	return fmt.Sprintf("%016X", binary.LittleEndian.Uint64(k.ID[:]))
}

// ParsePublicKey reads a minisign public key file (or just its base64 line).
func ParsePublicKey(text string) (PublicKey, error) {
	var line string
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "untrusted comment:") {
			line = l
			break
		}
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(raw) != 2+8+ed25519.PublicKeySize || string(raw[:2]) != "Ed" {
		return PublicKey{}, errors.New("not a minisign Ed25519 public key")
	}
	var k PublicKey
	copy(k.ID[:], raw[2:10])
	k.Key = ed25519.PublicKey(bytes.Clone(raw[10:]))
	return k, nil
}

// Verify checks a minisign signature of data against any of keys and
// returns its (signed) trusted comment and the key that made it.
func Verify(keys []PublicKey, data, sigFile []byte) (string, PublicKey, error) {
	trusted, key, err := verify(keys, data, sigFile)
	if err != nil {
		return "", PublicKey{}, err
	}
	return trusted, *key, nil
}

func verify(keys []PublicKey, data, sigFile []byte) (string, *PublicKey, error) {
	lines := strings.Split(strings.TrimRight(string(sigFile), "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "untrusted comment:") {
		return "", nil, errors.New("malformed signature file")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(sig) != 2+8+ed25519.SignatureSize {
		return "", nil, errors.New("malformed signature")
	}
	if string(sig[:2]) != "ED" {
		return "", nil, fmt.Errorf("unsupported signature algorithm %q (want prehashed ED)", sig[:2])
	}
	trusted, ok := strings.CutPrefix(strings.TrimRight(lines[2], "\r"), "trusted comment: ")
	if !ok {
		return "", nil, errors.New("signature has no trusted comment")
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return "", nil, errors.New("malformed trusted-comment signature")
	}

	var key *PublicKey
	for i := range keys {
		if bytes.Equal(keys[i].ID[:], sig[2:10]) {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return "", nil, fmt.Errorf("signed by unknown key %016X", binary.LittleEndian.Uint64(sig[2:10]))
	}
	hash := blake2b.Sum512(data)
	if !ed25519.Verify(key.Key, hash[:], sig[10:]) {
		return "", nil, errors.New("signature does not match")
	}
	if !ed25519.Verify(key.Key, append(bytes.Clone(sig[10:]), trusted...), global) {
		return "", nil, errors.New("trusted comment signature does not match")
	}
	return trusted, key, nil
}
