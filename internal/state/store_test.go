package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestStoreConcurrentJSONWritesRemainValid(t *testing.T) {
	store, err := NewAt(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.WorkspaceDir, "concurrent.json")
	var wg sync.WaitGroup
	for writer := 0; writer < 16; writer++ {
		writer := writer
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sequence := 0; sequence < 50; sequence++ {
				if err := store.WriteJSON(path, map[string]int{"writer": writer, "sequence": sequence}); err != nil {
					t.Errorf("write JSON: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]int
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("final JSON is invalid: %v; content=%q", err, raw)
	}
	if _, ok := value["writer"]; !ok {
		t.Fatalf("unexpected JSON: %#v", value)
	}
}

func TestStoreAppendLineSerializesConcurrentWriters(t *testing.T) {
	store, err := NewAt(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.WorkspaceDir, "events.log")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.AppendLine(path, []byte("event\n")); err != nil {
				t.Errorf("append: %v", err)
			}
		}()
	}
	wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, char := range raw {
		if char == '\n' {
			lines++
		}
	}
	if lines != 20 {
		t.Fatalf("line count = %d, want 20; content=%q", lines, raw)
	}
}

func TestWriteJSONUsesPrivateModeAndLeavesNoTempFile(t *testing.T) {
	store, err := NewAt(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.WorkspaceDir, "private.json")
	if err := store.WriteJSON(path, map[string]string{"secret": "redacted"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode = %o, want 600", got)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= 5 && entry.Name()[:5] == ".tmp-" {
			t.Fatalf("temporary file remained: %s", entry.Name())
		}
	}
}
