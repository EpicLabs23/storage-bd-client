package uiauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

// TOTP (RFC 6238) with the parameters every authenticator app supports:
// HMAC-SHA1, 6 digits, 30 s steps. One step either side is accepted for
// clock drift; the caller records the step used so a code can't be
// replayed.
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1

	BackupCodeCount = 8
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32 (no padding), as
// authenticator apps expect.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPURI is the otpauth:// URI shown as a QR code.
func TOTPURI(account, secret string) string {
	const issuer = "storage.bd backup"
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(totpDigits))
	v.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

func totpAt(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, n%1_000_000)
}

// CheckTOTP returns the time step code matches, and whether it matched.
// Steps <= lastStep are refused (already used).
func CheckTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil || len(key) == 0 {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for d := -totpSkew; d <= totpSkew; d++ {
		step := cur + int64(d)
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totpAt(key, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// TOTPCode is the current code; for tests.
func TOTPCode(secret string, now time.Time) string {
	key, _ := b32.DecodeString(secret)
	return totpAt(key, now.Unix()/totpPeriod)
}

// LooksLikeTOTP tells a 6-digit authenticator code from a backup code.
func LooksLikeTOTP(code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// NewBackupCodes returns BackupCodeCount single-use codes ("abcd-efgh-jkmn",
// 60 bits each) and their hashes, which are all that's stored. The codes
// are random, so a plain SHA-256 is enough; no slow hash needed.
func NewBackupCodes() (codes, hashes []string, err error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/l/i
	for i := 0; i < BackupCodeCount; i++ {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		var sb strings.Builder
		for j, x := range b {
			if j > 0 && j%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(x)%len(alphabet)])
		}
		codes = append(codes, sb.String())
		hashes = append(hashes, hashBackupCode(sb.String()))
	}
	return codes, hashes, nil
}

func hashBackupCode(c string) string {
	c = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
	sum := sha256.Sum256([]byte("storagebd-backup-code:" + c))
	return hex.EncodeToString(sum[:])
}

// MatchBackupCode returns the index of code's hash in hashes, or -1.
func MatchBackupCode(code string, hashes []string) int {
	h := hashBackupCode(code)
	found := -1
	for i, x := range hashes {
		if subtle.ConstantTimeCompare([]byte(h), []byte(x)) == 1 {
			found = i
		}
	}
	return found
}

// QRSVG renders text as a QR code SVG (for the web page).
func QRSVG(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	n := c.Size + 2*quiet
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&sb, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&sb, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	sb.WriteString(`"/></svg>`)
	return sb.String(), nil
}

// QRTerminal renders text as a QR code with Unicode half blocks, two
// rows per line. Colours are set explicitly (white blocks on black), so
// it scans on light and dark terminal themes alike.
func QRTerminal(text string) (string, error) {
	c, err := qr.Encode(text, qr.L)
	if err != nil {
		return "", err
	}
	const quiet = 2
	black := func(x, y int) bool { return c.Black(x-quiet, y-quiet) }
	n := c.Size + 2*quiet
	var sb strings.Builder
	for y := 0; y < n; y += 2 {
		sb.WriteString("\x1b[97;40m")
		for x := 0; x < n; x++ {
			top, bot := black(x, y), y+1 < n && black(x, y+1)
			switch {
			case !top && !bot:
				sb.WriteString("█")
			case top && !bot:
				sb.WriteString("▄")
			case !top && bot:
				sb.WriteString("▀")
			default:
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\x1b[0m\n")
	}
	return sb.String(), nil
}
