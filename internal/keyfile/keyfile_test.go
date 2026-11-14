package keyfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "signing.key")
	priv, err := NewEd25519(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 600", st.Mode().Perm())
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 700", dir.Mode().Perm())
	}

	got, err := ReadEd25519(path)
	if err != nil || !got.Equal(priv) {
		t.Fatalf("read back: %v", err)
	}

	if _, err := NewEd25519(path); !errors.Is(err, ErrExists) {
		t.Fatalf("second write: err = %v, want ErrExists", err)
	}
	again, _ := ReadEd25519(path)
	if !again.Equal(priv) {
		t.Fatal("key changed after refused overwrite")
	}
}

func TestReadRefusesReadableKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	if _, err := NewEd25519(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("read a mode-644 key")
	}
}
