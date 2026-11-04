package ticket

import (
	"bytes"
	"strings"
	"testing"
)

func TestCredentialIssueRevealAndDigest(t *testing.T) {
	cipher, err := NewCredentialCipher(map[int16][]byte{1: bytes.Repeat([]byte{0x42}, 32)}, 1)
	if err != nil {
		t.Fatalf("NewCredentialCipher() error = %v", err)
	}

	first, err := cipher.Issue(bookingIDForTest, seatIDForTest)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	second, err := cipher.Issue(bookingIDForTest, seatIDForTest)
	if err != nil {
		t.Fatalf("Issue() second error = %v", err)
	}
	if bytes.Equal(first.Digest, second.Digest) {
		t.Fatal("Issue() generated duplicate bearer credentials")
	}

	value, err := cipher.Reveal(bookingIDForTest, seatIDForTest, first)
	if err != nil {
		t.Fatalf("Reveal() error = %v", err)
	}
	if strings.ContainsAny(value, "+/=") {
		t.Fatalf("Reveal() = %q, want unpadded URL-safe base64", value)
	}

	digest, err := DigestCredential(value)
	if err != nil {
		t.Fatalf("DigestCredential() error = %v", err)
	}
	if !bytes.Equal(digest, first.Digest) {
		t.Fatal("DigestCredential() does not match the persisted digest")
	}
}

func TestCredentialRevealRejectsTamperingAndDifferentTicket(t *testing.T) {
	cipher, err := NewCredentialCipher(map[int16][]byte{1: bytes.Repeat([]byte{0x24}, 32)}, 1)
	if err != nil {
		t.Fatalf("NewCredentialCipher() error = %v", err)
	}
	issued, err := cipher.Issue(bookingIDForTest, seatIDForTest)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if _, err := cipher.Reveal(bookingIDForTest+1, seatIDForTest, issued); err == nil {
		t.Fatal("Reveal() with a different booking ID succeeded")
	}
	if _, err := cipher.Reveal(bookingIDForTest, seatIDForTest+1, issued); err == nil {
		t.Fatal("Reveal() with a different seat ID succeeded")
	}

	tampered := EncryptedCredential{
		Digest:     append([]byte(nil), issued.Digest...),
		Ciphertext: append([]byte(nil), issued.Ciphertext...),
		KeyVersion: issued.KeyVersion,
	}
	tampered.Ciphertext[len(tampered.Ciphertext)-1] ^= 0x01
	if _, err := cipher.Reveal(bookingIDForTest, seatIDForTest, tampered); err == nil {
		t.Fatal("Reveal() with modified ciphertext succeeded")
	}

	tampered = issued
	tampered.Digest = bytes.Repeat([]byte{0}, 32)
	if _, err := cipher.Reveal(bookingIDForTest, seatIDForTest, tampered); err == nil {
		t.Fatal("Reveal() with a mismatched digest succeeded")
	}
}

func TestCredentialCipherRequiresValidKeyRing(t *testing.T) {
	if _, err := NewCredentialCipher(nil, 1); err == nil {
		t.Fatal("NewCredentialCipher() with no keys succeeded")
	}
	for _, key := range [][]byte{[]byte("short"), bytes.Repeat([]byte{0x01}, 16)} {
		if _, err := NewCredentialCipher(map[int16][]byte{1: key}, 1); err == nil {
			t.Fatalf("NewCredentialCipher() with a %d-byte key succeeded", len(key))
		}
	}
	if _, err := NewCredentialCipher(map[int16][]byte{1: bytes.Repeat([]byte{0x01}, 32)}, 2); err == nil {
		t.Fatal("NewCredentialCipher() with a missing current key version succeeded")
	}
}

func TestCredentialCipherRevealsWithRetainedKeyVersion(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0x10}, 32)
	oldCipher, err := NewCredentialCipher(map[int16][]byte{1: oldKey}, 1)
	if err != nil {
		t.Fatalf("NewCredentialCipher() old key error = %v", err)
	}
	issued, err := oldCipher.Issue(bookingIDForTest, seatIDForTest)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	rotatedCipher, err := NewCredentialCipher(map[int16][]byte{
		1: oldKey,
		2: bytes.Repeat([]byte{0x20}, 32),
	}, 2)
	if err != nil {
		t.Fatalf("NewCredentialCipher() rotated key error = %v", err)
	}
	if _, err := rotatedCipher.Reveal(bookingIDForTest, seatIDForTest, issued); err != nil {
		t.Fatalf("Reveal() with retained key version error = %v", err)
	}
}

func TestDigestCredentialRejectsMalformedValue(t *testing.T) {
	for _, value := range []string{"", "short", strings.Repeat("a", 43) + "="} {
		if _, err := DigestCredential(value); err == nil {
			t.Errorf("DigestCredential(%q) succeeded", value)
		}
	}
}

const (
	bookingIDForTest = 41
	seatIDForTest    = 17
)
