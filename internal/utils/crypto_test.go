package utils

import (
	"encoding/hex"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	tests := []struct {
		name      string
		plainText string
		key       string
		wantErr   bool
	}{
		{
			name:      "Valid 32-byte key",
			plainText: "Hello World",
			key:       "12345678901234567890123456789012",
			wantErr:   false,
		},
		{
			name:      "Valid 64-char hex key",
			plainText: "Hello World",
			key:       hex.EncodeToString([]byte("12345678901234567890123456789012")),
			wantErr:   false,
		},
		{
			name:      "Invalid key length",
			plainText: "Hello World",
			key:       "shortkey",
			wantErr:   true,
		},
		{
			name:      "Empty text",
			plainText: "",
			key:       "12345678901234567890123456789012",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encrypted, err := Encrypt(tt.plainText, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("Encrypt() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			decrypted, err := Decrypt(encrypted, tt.key)
			if err != nil {
				t.Errorf("Decrypt() error = %v", err)
				return
			}

			if decrypted != tt.plainText {
				t.Errorf("Decrypt() = %v, want %v", decrypted, tt.plainText)
			}
		})
	}
}

func TestDecryptErrors(t *testing.T) {
	key := "12345678901234567890123456789012"

	t.Run("Invalid base64", func(t *testing.T) {
		_, err := Decrypt("invalid-base64", key)
		if err == nil {
			t.Error("Decrypt() expected error for invalid base64")
		}
	})

	t.Run("Short ciphertext", func(t *testing.T) {
		// Valid base64 but too short
		_, err := Decrypt("SHORT", key)
		if err == nil {
			t.Error("Decrypt() expected error for short ciphertext")
		}
	})
}
