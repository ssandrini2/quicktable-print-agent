// Package logfile is the agent's log on disk: one file per day, kept for a
// couple of weeks, and a way to read back its latest lines (what gets sent to
// QuickTable when someone has to look into a PC from afar).
package logfile

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	prefix     = "agent-"
	suffix     = ".log"
	dayLayout  = "2006-01-02"
	dirPerm    = 0o700
	filePerm   = 0o600
	maxDaySize = 10 << 20
)

// Before daily files, the agent wrote these; they go away once they're old.
var legacyNames = []string{"agent.log", "agent.log.1"}

// Log writes to the day's file in a directory. It is an io.Writer, safe for
// concurrent use.
type Log struct {
	dir      string
	keepDays int
	now      func() time.Time

	mu   sync.Mutex
	day  string
	file *os.File
	size int64
}

// Open starts logging in dir, keeping keepDays days of files. Older ones are
// removed now and every time the day changes.
func Open(dir string, keepDays int) (*Log, error) {
	return open(dir, keepDays, time.Now)
}

func open(dir string, keepDays int, now func() time.Time) (*Log, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}
	l := &Log{dir: dir, keepDays: keepDays, now: now}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.rotate(); err != nil {
		return nil, err
	}
	return l, nil
}

// Write appends p to today's file. A day that somehow fills maxDaySize (an
// error repeating without end) stops growing: the disk matters more.
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.now().Format(dayLayout) != l.day {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	if l.size >= maxDaySize {
		return len(p), nil
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	return n, err
}

// Close closes the current file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Tail returns the log's last lines, up to about maxBytes, oldest first,
// going back over previous days as far as that takes.
func (l *Log) Tail(maxBytes int) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	names, err := l.dayFiles()
	if err != nil {
		return "", err
	}
	var parts []string
	left := maxBytes
	for i := len(names) - 1; i >= 0 && left > 0; i-- {
		data, err := os.ReadFile(filepath.Join(l.dir, names[i]))
		if err != nil {
			return "", err
		}
		if len(data) > left {
			data = data[len(data)-left:]
			// Not from the middle of a line.
			if cut := strings.IndexByte(string(data), '\n'); cut >= 0 {
				data = data[cut+1:]
			}
		}
		left -= len(data)
		parts = append([]string{string(data)}, parts...)
	}
	return strings.Join(parts, ""), nil
}

// rotate opens today's file and removes what is too old. l.mu is held.
func (l *Log) rotate() error {
	now := l.now()
	day := now.Format(dayLayout)
	path := filepath.Join(l.dir, prefix+day+suffix)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, filePerm)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	l.file, l.day, l.size = file, day, info.Size()
	l.prune(now)
	return nil
}

// prune removes the files of days before the ones kept. Best effort: a file
// that can't be removed is tried again another day.
func (l *Log) prune(now time.Time) {
	oldest := now.AddDate(0, 0, -(l.keepDays - 1)).Format(dayLayout)
	names, _ := l.dayFiles()
	for _, name := range names {
		if day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix); day < oldest {
			_ = os.Remove(filepath.Join(l.dir, name))
		}
	}
	limit := now.AddDate(0, 0, -l.keepDays)
	for _, name := range legacyNames {
		path := filepath.Join(l.dir, name)
		if info, err := os.Stat(path); err == nil && info.ModTime().Before(limit) {
			_ = os.Remove(path)
		}
	}
}

// dayFiles lists the daily files, oldest first.
func (l *Log) dayFiles() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		if _, err := time.Parse(dayLayout, day); err == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
