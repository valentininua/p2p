package e2e

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

func DeriveRoomKey(password string) []byte {
	hash := sha256.Sum256([]byte(password))
	return hash[:]
}

func Encrypt(plaintext, key []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	ciphertext := aead.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func Decrypt(payload string, key []byte) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}

	nonceSize := aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	return aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
}
