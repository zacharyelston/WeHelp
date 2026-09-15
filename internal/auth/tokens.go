package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims is the access-token payload. RegisteredClaims.Subject is the user ID.
type Claims struct {
	TenantID uuid.UUID `json:"tid"`
	Role     string    `json:"role"`
	jwt.RegisteredClaims
}

// IssueAccessToken signs a short-lived JWT for the user.
func IssueAccessToken(userID, tenantID uuid.UUID, role string, secret []byte, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantID: tenantID,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

// ParseAccessToken verifies and decodes an access token.
func ParseAccessToken(tokenStr string, secret []byte) (*Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return nil, errors.New("invalid subject")
	}
	return &claims, nil
}

// NewRefreshToken returns an opaque URL-safe token plus the SHA-256 hash to
// store. Only the hash is persisted — the raw token is never recoverable.
func NewRefreshToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256(raw)
	return token, sum[:], nil
}

// HashRefreshToken hashes a presented refresh token for lookup. We hash the
// base64 text so lookups work even though the stored hash is of the raw bytes.
func HashRefreshToken(token string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("malformed refresh token: %w", err)
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}
