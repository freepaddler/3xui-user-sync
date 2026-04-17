package security

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Replace these constants before real production use.
var (
	serverCredentialKey = sha256.Sum256([]byte("3xui-user-sync-static-aes-key-change-me"))
	sessionSigningKey   = sha256.Sum256([]byte("3xui-user-sync-static-cookie-key-change-me"))
)

func Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(serverCredentialKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(serverCredentialKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, payload := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

type SessionClaims struct {
	ID        string
	ExpiresAt time.Time
}

func NewSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func SignSessionValue(id string, expiresAt time.Time) string {
	payload := id + "|" + expiresAt.UTC().Format(time.RFC3339Nano)
	mac := hmac.New(sha256.New, sessionSigningKey[:])
	_, _ = mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))
	return base64.StdEncoding.EncodeToString([]byte(payload + "|" + signature))
}

func ParseSessionValue(value string) (SessionClaims, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return SessionClaims{}, err
	}
	parts := split3(string(raw))
	if len(parts) != 3 {
		return SessionClaims{}, fmt.Errorf("invalid session cookie")
	}
	payload := parts[0] + "|" + parts[1]
	mac := hmac.New(sha256.New, sessionSigningKey[:])
	_, _ = mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return SessionClaims{}, fmt.Errorf("invalid session signature")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return SessionClaims{}, err
	}
	return SessionClaims{ID: parts[0], ExpiresAt: expiresAt}, nil
}

func split3(v string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(v); i++ {
		if v[i] != '|' {
			continue
		}
		out = append(out, v[start:i])
		start = i + 1
		if len(out) == 2 {
			out = append(out, v[start:])
			return out
		}
	}
	return nil
}

type sessionContextKey struct{}

func ContextWithSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, sessionID)
}

func SessionIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(sessionContextKey{}).(string)
	return v, ok
}
