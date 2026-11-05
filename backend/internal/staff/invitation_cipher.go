package staff

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

type InvitationTokenCipher struct {
	currentVersion int16
	aeadByVersion  map[int16]cipher.AEAD
}

func NewInvitationTokenCipher(keys map[int16][]byte, currentVersion int16) (*InvitationTokenCipher, error) {
	if currentVersion <= 0 || len(keys) == 0 {
		return nil, errors.New("a positive current invitation key version and at least one key are required")
	}

	aeads := make(map[int16]cipher.AEAD, len(keys))
	for version, key := range keys {
		if version <= 0 || len(key) != 32 {
			return nil, fmt.Errorf("invitation key version %d must identify a 32-byte AES key", version)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("create invitation AES cipher for key version %d: %w", version, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("create invitation AEAD for key version %d: %w", version, err)
		}
		aeads[version] = aead
	}

	if _, ok := aeads[currentVersion]; !ok {
		return nil, fmt.Errorf("current invitation key version %d is not configured", currentVersion)
	}

	return &InvitationTokenCipher{currentVersion: currentVersion, aeadByVersion: aeads}, nil
}

func (c *InvitationTokenCipher) Encrypt(email, token string) ([]byte, int16, error) {
	if c == nil {
		return nil, 0, errors.New("staff invitation token cipher is required")
	}

	aead := c.aeadByVersion[c.currentVersion]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, fmt.Errorf("generate invitation token nonce: %w", err)
	}

	sealed := aead.Seal(nil, nonce, []byte(token), invitationAssociatedData(email))
	return append(nonce, sealed...), c.currentVersion, nil
}

func (c *InvitationTokenCipher) Decrypt(email string, version int16, ciphertext []byte) (string, error) {
	if c == nil {
		return "", errors.New("staff invitation token cipher is required")
	}

	aead, ok := c.aeadByVersion[version]
	if !ok || aead == nil || len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("invalid encrypted staff invitation token")
	}

	nonce, sealed := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, sealed, invitationAssociatedData(email))
	if err != nil {
		return "", errors.New("invalid encrypted staff invitation token")
	}

	defer clear(plain)
	return string(plain), nil
}

func invitationAssociatedData(email string) []byte {
	return []byte("staff-invitation:v1:" + strings.ToLower(strings.TrimSpace(email)))
}
