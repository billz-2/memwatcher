package memwatcher_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/billz-2/memwatcher"
	"github.com/prometheus/client_golang/prometheus"
)

// makeWatcher создаёт минимальный Watcher с указанным dumpDir, maxDumps, dumpTTL.
func makeWatcher(t *testing.T, dumpDir string, maxDumps int, dumpTTL time.Duration) *memwatcher.Watcher {
	t.Helper()
	w, err := memwatcher.New(memwatcher.Config{
		ServiceName:   "test_svc",
		DumpDir:       dumpDir,
		PollInterval:  time.Second,
		CooldownTier2: time.Minute,
		CooldownTier3: 30 * time.Second,
		MaxDumps:      maxDumps,
		DumpTTL:       dumpTTL,
		Registerer:    prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

// makeDumpDir создаёт директорию дампа в dumpDir и возвращает её имя.
func makeDumpDir(t *testing.T, dumpDir, name string) string {
	t.Helper()
	path := filepath.Join(dumpDir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return name
}

func TestCleanup_Disabled(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 5; i++ {
		makeDumpDir(t, dir, "memdump-svc-2026030"+string(rune('0'+i))+"-100000")
	}

	w := makeWatcher(t, dir, 0, 0)
	w.Cleanup() // тест-хелпер из export_test.go

	entries, _ := os.ReadDir(dir)
	if len(entries) != 5 {
		t.Errorf("cleanup with MaxDumps=0,DumpTTL=0 deleted entries: got %d, want 5", len(entries))
	}
}

func TestCleanup_NoDir(t *testing.T) {
	w := makeWatcher(t, "/tmp/nonexistent-memwatcher-test-dir-xyz", 5, 0)
	w.Cleanup() // не должно быть паники
}

func TestCleanup_MaxDumps(t *testing.T) {
	dir := t.TempDir()

	dirs := []string{
		"memdump-svc-20260301-100000",
		"memdump-svc-20260302-100000",
		"memdump-svc-20260303-100000",
		"memdump-svc-20260304-100000",
	}
	for _, d := range dirs {
		makeDumpDir(t, dir, d)
	}

	w := makeWatcher(t, dir, 2, 0)
	w.Cleanup()

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("after cleanup(MaxDumps=2) got %d dirs, want 1", len(entries))
	}

	if len(entries) > 0 && entries[0].Name() != "memdump-svc-20260304-100000" {
		t.Errorf("wrong dir survived: %s", entries[0].Name())
	}
}

func TestCleanup_DumpTTL(t *testing.T) {
	dir := t.TempDir()

	oldDir := makeDumpDir(t, dir, "memdump-svc-20260301-100000")
	newDir := makeDumpDir(t, dir, "memdump-svc-20260302-100000")

	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, oldDir), oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	w := makeWatcher(t, dir, 0, time.Hour)
	w.Cleanup()

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("after TTL cleanup got %d dirs, want 1", len(entries))
	}
	if len(entries) > 0 && entries[0].Name() != newDir {
		t.Errorf("wrong dir survived: got %s, want %s", entries[0].Name(), newDir)
	}
}

func TestCleanup_IgnoresNonDumpDirs(t *testing.T) {
	dir := t.TempDir()

	notADump := filepath.Join(dir, "some-other-dir")
	if err := os.MkdirAll(notADump, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	makeDumpDir(t, dir, "memdump-svc-20260301-100000")

	w := makeWatcher(t, dir, 3, 0)
	w.Cleanup()

	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2 (memdump + some-other-dir)", len(entries))
	}
}
