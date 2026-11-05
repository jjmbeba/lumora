package staff

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	passwordIterations = 600_000
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
	totpSecretBytes    = 20
	totpDigits         = 6
	totpPeriod         = 30 * time.Second
	recoveryCodeCount  = 10
)

// HashPassword derives a PBKDF2-HMAC-SHA-256 hash with a unique random salt.
func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 8 || len(password) > 1024 {
		return "", errors.New("password must contain at least 8 characters and at most 1024 bytes")
	}

	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	derived, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyBytes)
	if err != nil {
		clear(salt)
		return "", fmt.Errorf("derive password hash: %w", err)
	}

	encoded := fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derived))
	clear(salt)
	clear(derived)

	return encoded, nil
}

func VerifyPassword(encoded, password string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return ErrInvalidCredentials
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100_000 || iterations > 2_000_000 {
		return ErrInvalidCredentials
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) != passwordSaltBytes {
		clear(salt)
		return ErrInvalidCredentials
	}

	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) != passwordKeyBytes {
		clear(salt)
		clear(want)
		return ErrInvalidCredentials
	}

	defer clear(salt)
	defer clear(want)

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		clear(got)
		return ErrInvalidCredentials
	}

	defer clear(got)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidCredentials
	}

	return nil
}

func verifyDummyPassword(password string) {
	salt := make([]byte, passwordSaltBytes)
	derived, _ := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyBytes)

	clear(salt)
	clear(derived)
}

func NewTOTPSecret() (string, error) {
	secret := make([]byte, totpSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}

	defer clear(secret)
	return base32.StdEncoding.EncodeToString(secret), nil
}

func VerifyTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}

	for _, char := range code {
		if char < '0' || char > '9' {
			return false
		}
	}

	key, err := base32.StdEncoding.DecodeString(strings.ToUpper(secret))
	if err != nil || len(key) == 0 {
		return false
	}

	defer clear(key)
	step := now.Unix() / int64(totpPeriod.Seconds())

	for offset := int64(-1); offset <= 1; offset++ {
		var counter [8]byte

		binary.BigEndian.PutUint64(counter[:], uint64(step+offset))
		mac := hmac.New(sha1.New, key)

		_, _ = mac.Write(counter[:])
		sum := mac.Sum(nil)
		index := sum[len(sum)-1] & 0x0f
		value := (binary.BigEndian.Uint32(sum[index:index+4]) & 0x7fffffff) % 1_000_000

		candidate := fmt.Sprintf("%06d", value)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}

	return false
}

func NewRecoveryCodes() ([]string, [][]byte, error) {
	codes := make([]string, 0, recoveryCodeCount)
	digests := make([][]byte, 0, recoveryCodeCount)

	for range recoveryCodeCount {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}

		code := base32.StdEncoding.EncodeToString(raw)
		digest := sha256.Sum256([]byte(code))

		codes = append(codes, code)
		digests = append(digests, append([]byte(nil), digest[:]...))
		clear(raw)
	}

	return codes, digests, nil
}

func digestToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return append([]byte(nil), digest[:]...)
}
