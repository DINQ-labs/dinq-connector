package auth

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCredentialCipherRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatalf("NewCredentialCipher: %v", err)
	}

	plaintext := `{"email":"user@example.com","password":"secret"}`
	encrypted, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(encrypted, encryptedCredentialPrefix) {
		t.Fatalf("encrypted value is missing prefix: %q", encrypted)
	}
	if strings.Contains(encrypted, "secret") {
		t.Fatal("encrypted value contains plaintext password")
	}

	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("Decrypt = %q, want %q", decrypted, plaintext)
	}
}

func TestCredentialCipherRejectsInvalidInputs(t *testing.T) {
	if _, err := NewCredentialCipher("short"); err == nil {
		t.Fatal("expected invalid key error")
	}

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatalf("NewCredentialCipher: %v", err)
	}
	if _, err := cipher.Decrypt(`{"password":"plaintext"}`); err == nil {
		t.Fatal("expected plaintext credential rejection")
	}
}
