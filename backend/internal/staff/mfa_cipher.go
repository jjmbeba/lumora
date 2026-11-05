package staff

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

type MFACipher struct {
	currentVersion int16
	aeadByVersion  map[int16]cipher.AEAD
}

func NewMFACipher(keys map[int16][]byte, currentVersion int16) (*MFACipher, error) {
	if currentVersion <= 0 || len(keys) == 0 {
		return nil, errors.New("a positive current MFA key version and at least one key are required")
	}

	aeads := make(map[int16]cipher.AEAD, len(keys))
	for version, key := range keys {
		if version <= 0 || len(key) != 32 {
			return nil, fmt.Errorf("MFA key version %d must identify a 32-byte AES key", version)
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("create MFA AES cipher for key version %d: %w", version, err)
		}

		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("create MFA AEAD for key version %d: %w", version, err)
		}

		aeads[version] = aead
	}

	if _, ok := aeads[currentVersion]; !ok {
		return nil, fmt.Errorf("current MFA key version %d is not configured", currentVersion)
	}

	return &MFACipher{currentVersion: currentVersion, aeadByVersion: aeads}, nil
}

func (c *MFACipher) Encrypt(email, secret string) ([]byte, int16, error) {
	if c == nil {
		return nil, 0, errors.New("MFA cipher is required")
	}

	aead := c.aeadByVersion[c.currentVersion]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, fmt.Errorf("generate MFA nonce: %w", err)
	}

	sealed := aead.Seal(nil, nonce, []byte(secret), mfaAssociatedData(email))

	return append(nonce, sealed...), c.currentVersion, nil
}

func (c *MFACipher) Decrypt(email string, version int16, ciphertext []byte) (string, error) {
	if c == nil {
		return "", errors.New("MFA cipher is required")
	}

	aead, ok := c.aeadByVersion[version]
	if !ok || aead == nil || len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("invalid encrypted MFA secret")
	}

	nonce, sealed := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	secret, err := aead.Open(nil, nonce, sealed, mfaAssociatedData(email))
	if err != nil {
		return "", errors.New("invalid encrypted MFA secret")
	}

	defer clear(secret)

	return string(secret), nil
}

func mfaAssociatedData(email string) []byte {
	return []byte("staff-totp:v1:" + strings.ToLower(strings.TrimSpace(email)))
}
