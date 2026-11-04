package ticket

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
)

const credentialBytes = 32

// ErrInvalidCredential indicates a malformed or unauthentic ticket credential.
var ErrInvalidCredential = errors.New("invalid ticket credential")

// EncryptedCredential is the material persisted for a ticket. The plaintext
// bearer credential is returned only by Reveal and must never be logged.
type EncryptedCredential struct {
	Digest     []byte
	Ciphertext []byte
	KeyVersion int16
}

// CredentialCipher generates opaque ticket credentials and encrypts them for
// later rendering. Keep the supplied encryption keys outside PostgreSQL.
type CredentialCipher struct {
	currentVersion int16
	aeadByVersion  map[int16]cipher.AEAD
}

// NewCredentialCipher builds a key ring. AES-256 keys must be exactly 32
// bytes, and currentVersion must identify the key used for new credentials.
func NewCredentialCipher(keys map[int16][]byte, currentVersion int16) (*CredentialCipher, error) {
	if currentVersion <= 0 {
		return nil, errors.New("ticket credential key version must be positive")
	}
	if len(keys) == 0 {
		return nil, errors.New("at least one ticket credential key is required")
	}

	aeadByVersion := make(map[int16]cipher.AEAD, len(keys))
	for version, key := range keys {
		if version <= 0 {
			return nil, fmt.Errorf("ticket credential key version %d must be positive", version)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("ticket credential key version %d must be a 32-byte AES-256 key", version)
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("create AES cipher for ticket key version %d: %w", version, err)
		}

		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("create GCM cipher for ticket key version %d: %w", version, err)
		}

		aeadByVersion[version] = aead
	}
	if _, ok := aeadByVersion[currentVersion]; !ok {
		return nil, fmt.Errorf("current ticket credential key version %d is not configured", currentVersion)
	}

	return &CredentialCipher{
		currentVersion: currentVersion,
		aeadByVersion:  aeadByVersion,
	}, nil
}

// Issue generates a 256-bit random bearer value, hashes the raw random bytes,
// and encrypts those bytes with the current key. Ciphertext authentication is
// bound to the ticket's booking and seat so it cannot be swapped to another
// ticket row without detection.
func (c *CredentialCipher) Issue(bookingID, seatID int) (EncryptedCredential, error) {
	if err := validateTicketIDs(bookingID, seatID); err != nil {
		return EncryptedCredential{}, err
	}
	if c == nil {
		return EncryptedCredential{}, errors.New("ticket credential cipher is required")
	}

	secret := make([]byte, credentialBytes)
	if _, err := rand.Read(secret); err != nil {
		return EncryptedCredential{}, fmt.Errorf("generate ticket credential: %w", err)
	}
	defer clear(secret)

	aead, ok := c.aeadByVersion[c.currentVersion]
	if !ok || aead == nil {
		return EncryptedCredential{}, errors.New("ticket credential cipher is not initialized")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return EncryptedCredential{}, fmt.Errorf("generate ticket credential nonce: %w", err)
	}

	digest := sha256.Sum256(secret)
	ciphertext := append(nonce, aead.Seal(nil, nonce, secret, ticketAssociatedData(bookingID, seatID))...)

	return EncryptedCredential{
		Digest:     append([]byte(nil), digest[:]...),
		Ciphertext: ciphertext,
		KeyVersion: c.currentVersion,
	}, nil
}

// Reveal decrypts an issued credential for email or booking-detail rendering.
// It also verifies the decrypted bytes match the persisted digest.
func (c *CredentialCipher) Reveal(bookingID, seatID int, stored EncryptedCredential) (string, error) {
	if err := validateTicketIDs(bookingID, seatID); err != nil {
		return "", err
	}
	if c == nil {
		return "", errors.New("ticket credential cipher is required")
	}

	aead, ok := c.aeadByVersion[stored.KeyVersion]
	if !ok || aead == nil {
		return "", fmt.Errorf("ticket credential key version %d is not configured", stored.KeyVersion)
	}

	if len(stored.Ciphertext) < aead.NonceSize()+aead.Overhead() {
		return "", ErrInvalidCredential
	}

	nonce := stored.Ciphertext[:aead.NonceSize()]
	sealed := stored.Ciphertext[aead.NonceSize():]
	secret, err := aead.Open(nil, nonce, sealed, ticketAssociatedData(bookingID, seatID))

	if err != nil {
		return "", fmt.Errorf("%w: encrypted value failed authentication", ErrInvalidCredential)
	}
	defer clear(secret)

	if len(secret) != credentialBytes || len(stored.Digest) != sha256.Size {
		return "", ErrInvalidCredential
	}

	digest := sha256.Sum256(secret)
	if subtle.ConstantTimeCompare(digest[:], stored.Digest) != 1 {
		return "", ErrInvalidCredential
	}

	return base64.RawURLEncoding.EncodeToString(secret), nil
}

// DigestCredential validates the URL-safe ticket value and returns the SHA-256
// digest of its decoded random bytes, ready for a database lookup.
func DigestCredential(value string) ([]byte, error) {
	secret, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(secret) != credentialBytes || base64.RawURLEncoding.EncodeToString(secret) != value {
		clear(secret)
		return nil, ErrInvalidCredential
	}
	defer clear(secret)

	digest := sha256.Sum256(secret)
	return append([]byte(nil), digest[:]...), nil
}

func validateTicketIDs(bookingID, seatID int) error {
	if bookingID <= 0 || seatID <= 0 {
		return errors.New("booking ID and seat ID must be positive")
	}
	return nil
}

func ticketAssociatedData(bookingID, seatID int) []byte {
	return fmt.Appendf(nil, "cinema-ticket-credential:v1:booking:%d:seat:%d", bookingID, seatID)
}
