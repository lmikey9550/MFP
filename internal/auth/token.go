package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type contextKey string

const ClaimsContextKey contextKey = "mfp_claims"

type Claims struct {
	Username       string    `json:"username"`
	Role           string    `json:"role"`
	SessionVersion int       `json:"session_version"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret []byte, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: secret, ttl: ttl}
}

func (m *TokenManager) Issue(username, role string, sessionVersion int) (string, error) {
	claims := Claims{
		Username:       username,
		Role:           role,
		SessionVersion: sessionVersion,
		ExpiresAt:      time.Now().UTC().Add(m.ttl),
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	signature := sign(m.secret, payload)
	return payload + "." + signature, nil
}

func (m *TokenManager) Parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, errors.New("invalid token")
	}
	payload, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sign(m.secret, payload)), []byte(sig)) {
		return Claims{}, errors.New("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Claims{}, err
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return Claims{}, err
	}
	if time.Now().UTC().After(claims.ExpiresAt) {
		return Claims{}, errors.New("token expired")
	}
	return claims, nil
}

func WithClaims(ctx context.Context, claims Claims) context.Context {
	return context.WithValue(ctx, ClaimsContextKey, claims)
}

func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	claims, ok := ctx.Value(ClaimsContextKey).(Claims)
	return claims, ok
}

func sign(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
