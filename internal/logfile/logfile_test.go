package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func day(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse("2006-01-02 15:04", value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestWritesOneFilePerDayAndDropsOldOnes(t *testing.T) {
	dir := t.TempDir()
	c := &clock{at: day(t, "2026-10-01 23:50")}
	log, err := open(dir, 3, c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	write := func(line string) {
		t.Helper()
		if _, err := log.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	write("day one")
	c.at = day(t, "2026-10-02 00:10")
	write("day two")
	c.at = day(t, "2026-10-03 09:00")
	write("day three")
	if got := strings.Join(files(t, dir), ","); got != "agent-2026-10-01.log,agent-2026-10-02.log,agent-2026-10-03.log" {
		t.Fatalf("files = %s", got)
	}

	// The fourth day pushes the first one out.
	c.at = day(t, "2026-10-04 09:00")
	write("day four")
	if got := strings.Join(files(t, dir), ","); got != "agent-2026-10-02.log,agent-2026-10-03.log,agent-2026-10-04.log" {
		t.Fatalf("files = %s", got)
	}
}

func TestKeepsWritingToTheSameDayAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	c := &clock{at: day(t, "2026-10-01 10:00")}
	for _, line := range []string{"first run\n", "second run\n"} {
		log, err := open(dir, 14, c.now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		log.Close()
	}
	data, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-01.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first run\nsecond run\n" {
		t.Fatalf("got %q", data)
	}
}

func TestTailReadsBackAcrossDaysFromALineStart(t *testing.T) {
	dir := t.TempDir()
	c := &clock{at: day(t, "2026-10-01 10:00")}
	log, err := open(dir, 14, c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.Write([]byte("one: aaaa\none: bbbb\n"))
	c.at = day(t, "2026-10-02 10:00")
	log.Write([]byte("two: cccc\n"))

	all, err := log.Tail(1000)
	if err != nil {
		t.Fatal(err)
	}
	if all != "one: aaaa\none: bbbb\ntwo: cccc\n" {
		t.Fatalf("got %q", all)
	}
	// Room for the last day and part of the one before: whole lines only.
	some, err := log.Tail(len("two: cccc\n") + len("one: bbbb\n") + 3)
	if err != nil {
		t.Fatal(err)
	}
	if some != "one: bbbb\ntwo: cccc\n" {
		t.Fatalf("got %q", some)
	}
}

func TestRemovesTheOldSingleFileOnceItIsOld(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "agent.log")
	recent := filepath.Join(dir, "agent.log.1")
	for _, path := range []string{old, recent} {
		if err := os.WriteFile(path, []byte("before daily files\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := day(t, "2026-10-20 10:00")
	os.Chtimes(old, now.AddDate(0, 0, -30), now.AddDate(0, 0, -30))
	os.Chtimes(recent, now.AddDate(0, 0, -2), now.AddDate(0, 0, -2))

	log, err := open(dir, 14, (&clock{at: now}).now)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if got := strings.Join(files(t, dir), ","); got != "agent-2026-10-20.log,agent.log.1" {
		t.Fatalf("files = %s", got)
	}
}
