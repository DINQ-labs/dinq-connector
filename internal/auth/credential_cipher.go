package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

const encryptedCredentialPrefix = "enc:v1:"

// CredentialCipher encrypts user-provided mailbox passwords before they are
// stored in connected_accounts.access_token.
type CredentialCipher struct {
	aead cipher.AEAD
}

func NewCredentialCipher(encodedKey string) (*CredentialCipher, error) {
	encodedKey = strings.TrimSpace(encodedKey)
	if encodedKey == "" {
		return nil, fmt.Errorf("SMTP_CREDENTIALS_ENCRYPTION_KEY is required")
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("SMTP_CREDENTIALS_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &CredentialCipher{aead: aead}, nil
}

func (c *CredentialCipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(nonce, sealed...)
	return encryptedCredentialPrefix + base64.RawStdEncoding.EncodeToString(payload), nil
}

func (c *CredentialCipher) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, encryptedCredentialPrefix) {
		return "", fmt.Errorf("stored credentials are not encrypted; reconnect the mailbox")
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedCredentialPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted credentials: %w", err)
	}
	if len(payload) < c.aead.NonceSize() {
		return "", fmt.Errorf("encrypted credentials are invalid")
	}
	nonce, ciphertext := payload[:c.aead.NonceSize()], payload[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt credentials: %w", err)
	}
	return string(plaintext), nil
}
