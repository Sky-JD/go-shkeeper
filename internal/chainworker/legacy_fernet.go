package chainworker

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

func decryptLegacyFernetSecret(password string, encoded string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", errors.New("legacy account password is required")
	}
	outer, err := decodeBase64URL(encoded)
	if err != nil {
		return "", fmt.Errorf("decode outer legacy secret: %w", err)
	}
	token, err := decodeBase64URL(string(outer))
	if err != nil {
		return "", fmt.Errorf("decode legacy fernet token: %w", err)
	}
	if len(token) < 1+8+16+aes.BlockSize+sha256.Size || token[0] != 0x80 {
		return "", errors.New("legacy fernet token is invalid")
	}
	key := pbkdf2.Key([]byte(password), []byte("Shkeeper4TheWin!"), 500000, 32, sha256.New)
	signingKey := key[:16]
	encryptionKey := key[16:]
	macStart := len(token) - sha256.Size
	if macStart <= 1+8+16 {
		return "", errors.New("legacy fernet token is truncated")
	}
	mac := hmac.New(sha256.New, signingKey)
	_, _ = mac.Write(token[:macStart])
	if !hmac.Equal(mac.Sum(nil), token[macStart:]) {
		return "", errors.New("legacy fernet token signature mismatch")
	}
	iv := token[9:25]
	ciphertext := token[25:macStart]
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("legacy fernet ciphertext is not block aligned")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)
	plaintext, err = pkcs7Unpad(plaintext, aes.BlockSize)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func decodeBase64URL(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty base64 value")
	}
	if decoded, err := base64.URLEncoding.DecodeString(padBase64(value)); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(value)
}

func padBase64(value string) string {
	switch len(value) % 4 {
	case 2:
		return value + "=="
	case 3:
		return value + "="
	default:
		return value
	}
}

func pkcs7Unpad(value []byte, blockSize int) ([]byte, error) {
	if len(value) == 0 || len(value)%blockSize != 0 {
		return nil, errors.New("invalid PKCS7 payload size")
	}
	pad := int(value[len(value)-1])
	if pad == 0 || pad > blockSize || pad > len(value) {
		return nil, errors.New("invalid PKCS7 padding")
	}
	for _, b := range value[len(value)-pad:] {
		if int(b) != pad {
			return nil, errors.New("invalid PKCS7 padding bytes")
		}
	}
	return value[:len(value)-pad], nil
}
