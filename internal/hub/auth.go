package hub

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// GenerateToken returns a fresh node auth token: 32 bytes of entropy encoded
// with URL-safe base64 (no padding).
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("hub: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken returns the bcrypt hash of a token, suitable for node.token_hash.
func HashToken(token string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hub: hash token: %w", err)
	}
	return string(hash), nil
}

// CheckToken reports whether token matches a stored bcrypt hash.
func CheckToken(token, hash string) bool {
	if token == "" || hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)) == nil
}
