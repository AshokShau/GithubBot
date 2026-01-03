package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
)

// Encrypt encrypts plain text using AES-GCM
func Encrypt(plainText, keyString string) (string, error) {
	key, err := getKeyBytes(keyString)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plainText), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts base64 encoded ciphertext using AES-GCM
func Decrypt(encryptedText, keyString string) (string, error) {
	key, err := getKeyBytes(keyString)
	if err != nil {
		return "", err
	}

	data, err := base64.StdEncoding.DecodeString(encryptedText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

func getKeyBytes(keyString string) ([]byte, error) {
	if len(keyString) == 64 {
		return hex.DecodeString(keyString)
	}

	if len(keyString) == 32 || len(keyString) == 24 || len(keyString) == 16 {
		return []byte(keyString), nil
	}
	return nil, errors.New("invalid key length: must be 32 bytes (raw) or 64 hex chars")
}
