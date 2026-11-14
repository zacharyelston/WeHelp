// Package logfile is a daily log file with fixed retention (#42): one file
// per day, mode 600, in a directory of its own, and files older than the
// retention deleted as days roll over. The Relay keeps 7 days.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Daily writes to <dir>/<prefix>-YYYY-MM-DD.log, switching files at
// midnight UTC and keeping the last Keep days.
type Daily struct {
	dir, prefix string
	keep        int
	now         func() time.Time

	mu  sync.Mutex
	day string
	f   *os.File
}

// Open creates dir (mode 700) and opens today's file. keep is in days.
func Open(dir, prefix string, keep int) (*Daily, error) {
	return open(dir, prefix, keep, time.Now)
}

func open(dir, prefix string, keep int, now func() time.Time) (*Daily, error) {
	if keep < 1 {
		return nil, fmt.Errorf("logfile: keep must be at least 1 day")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	d := &Daily{dir: dir, prefix: prefix, keep: keep, now: now}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.rotateLocked(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Daily) name(day string) string { return filepath.Join(d.dir, d.prefix+"-"+day+".log") }

func (d *Daily) rotateLocked() error {
	day := d.now().UTC().Format("2006-01-02")
	if d.f != nil && day == d.day {
		return nil
	}
	f, err := os.OpenFile(d.name(day), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if d.f != nil {
		_ = d.f.Close()
	}
	d.f, d.day = f, day
	d.pruneLocked()
	return nil
}

// pruneLocked deletes this prefix's files older than keep days.
func (d *Daily) pruneLocked() {
	matches, _ := filepath.Glob(filepath.Join(d.dir, d.prefix+"-*.log"))
	sort.Strings(matches)
	cutoff := d.now().UTC().AddDate(0, 0, -(d.keep - 1)).Format("2006-01-02")
	for _, m := range matches {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), d.prefix+"-"), ".log")
		if _, err := time.Parse("2006-01-02", day); err == nil && day < cutoff {
			_ = os.Remove(m)
		}
	}
}

// Write appends p to today's file.
func (d *Daily) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.rotateLocked(); err != nil {
		return 0, err
	}
	return d.f.Write(p)
}

// Close closes the current file.
func (d *Daily) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return nil
	}
	err := d.f.Close()
	d.f = nil
	return err
}
