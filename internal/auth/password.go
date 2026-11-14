// Package auth provides password hashing and token issuance/verification.
package auth

import "github.com/zacharyelston/wehelp/internal/staffauth"

// HashPassword returns a PHC-format argon2id hash (staffauth holds the
// parameters, shared with the Registry console).
func HashPassword(password string) (string, error) { return staffauth.HashPassword(password) }

// VerifyPassword reports whether password matches the PHC-format hash.
func VerifyPassword(password, hash string) (bool, error) {
	return staffauth.VerifyPassword(password, hash)
}
