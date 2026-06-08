package todo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTripAndBadFileBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.Local)
	if err := store.Save([]Item{{ID: "abc123", Title: "test", Priority: PriorityHigh, Status: StatusOpen, CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "test" {
		t.Fatalf("unexpected items: %#v", items)
	}

	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("expected parse error")
	}
	matches, err := filepath.Glob(path + ".bad-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected backup file, got %v", matches)
	}
}
