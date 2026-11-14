package logfile

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestDailyRotationAndRetention(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	now := time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC)
	d, err := open(dir, "relay", 7, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// An unrelated file in the directory is left alone.
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)

	for i := 0; i < 10; i++ {
		if _, err := d.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
		now = now.Add(24 * time.Hour)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "relay-*.log"))
	sort.Strings(files)
	if len(files) != 7 {
		t.Fatalf("kept %d files, want 7: %v", len(files), files)
	}
	if filepath.Base(files[0]) != "relay-2026-11-04.log" || filepath.Base(files[6]) != "relay-2026-11-10.log" {
		t.Errorf("kept %s … %s", filepath.Base(files[0]), filepath.Base(files[6]))
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("an unrelated file was removed")
	}
	info, _ := os.Stat(files[6])
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 600", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(dir)
	if dinfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 700", dinfo.Mode().Perm())
	}
}
