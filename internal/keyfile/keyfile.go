// Package keyfile stores private keys as PEM files with mode 600 in a
// mode-700 directory, refuses to overwrite an existing key, and refuses to
// load a key that group or others can read. Used by the Registry signing
// key (#60) and the office keys (#35).
package keyfile

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrExists is returned when a key file is already present.
var ErrExists = errors.New("keyfile: key already exists; refusing to overwrite")

// Write stores key (PKCS#8) at path. It never replaces an existing file.
func Write(path string, key crypto.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("keyfile: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("keyfile: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("keyfile: %w", err)
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return fmt.Errorf("keyfile: write: %w", err)
	}
	return f.Close()
}

// Read loads a PKCS#8 private key, refusing files readable by group or others.
func Read(path string) (crypto.PrivateKey, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("keyfile: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("keyfile: %s is mode %v; it must not be readable by group or others (chmod 600)", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("keyfile: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("keyfile: %s is not a PEM PRIVATE KEY", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("keyfile: parse: %w", err)
	}
	return key, nil
}

// NewEd25519 generates an ed25519 key and writes it to path.
func NewEd25519(path string) (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := Write(path, priv); err != nil {
		return nil, err
	}
	return priv, nil
}

// ReadEd25519 loads an ed25519 key written by NewEd25519.
func ReadEd25519(path string) (ed25519.PrivateKey, error) {
	key, err := Read(path)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("keyfile: %s is not an ed25519 key", path)
	}
	return priv, nil
}
