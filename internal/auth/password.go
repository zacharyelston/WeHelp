// Package auth provides password hashing and token issuance/verification.
package auth

import (
	"github.com/alexedwards/argon2id"
)

// Params follow the RFC 9106 second recommendation (m=19MiB, t=2, p=1) —
// modest enough for small on-prem servers, still solidly memory-hard.
var params = &argon2id.Params{
	Memory:      19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(password string) (string, error) {
	return argon2id.CreateHash(password, params)
}

// VerifyPassword reports whether password matches the PHC-format hash.
func VerifyPassword(password, hash string) (bool, error) {
	return argon2id.ComparePasswordAndHash(password, hash)
}
