package agent

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countingReader struct {
	reader io.Reader
	bytes  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func TestReadTextSelectionStopsAfterRequestedLines(t *testing.T) {
	data := strings.Repeat("line content that should not all be read\n", 100_000)
	reader := &countingReader{reader: strings.NewReader(data)}
	content, scanned, returned, truncated, reachedEOF, err := readTextSelection(reader, 1, 2, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if returned != 2 || scanned != 2 {
		t.Fatalf("scanned=%d returned=%d, want 2/2", scanned, returned)
	}
	if truncated || reachedEOF {
		t.Fatalf("truncated=%v reachedEOF=%v, want false/false", truncated, reachedEOF)
	}
	if strings.Count(content, "\n") != 1 {
		t.Fatalf("unexpected content: %q", content)
	}
	if reader.bytes >= len(data)/10 {
		t.Fatalf("reader consumed %d of %d bytes; ranged read did not stop early", reader.bytes, len(data))
	}
}

func TestReadTextSelectionStopsAtCharacterLimit(t *testing.T) {
	data := strings.Repeat("abcdefghij\n", 100_000)
	reader := &countingReader{reader: strings.NewReader(data)}
	content, scanned, returned, truncated, reachedEOF, err := readTextSelection(reader, 1, 0, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > 25 {
		t.Fatalf("content length = %d, want <= 25", len(content))
	}
	if scanned == 0 || returned == 0 || !truncated || reachedEOF {
		t.Fatalf("scanned=%d returned=%d truncated=%v reachedEOF=%v", scanned, returned, truncated, reachedEOF)
	}
	if reader.bytes >= len(data)/10 {
		t.Fatalf("reader consumed %d of %d bytes; bounded read did not stop early", reader.bytes, len(data))
	}
}

func TestReadManyBoundsWorkByBatchBudget(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat("abcdefghij\n", 1_000)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runtime := newIsolatedRuntime(t, "batch-read", root, t.TempDir(), "full")
	runtime.Config.MaxReadChars = 10_000
	runtime.Config.MaxBatchReadChars = 100
	value, err := runtime.readMany(map[string]any{
		"paths": []any{"one.txt", "two.txt"}, "max_chars_per_file": 10_000, "concurrency": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["chars_returned"].(int) > 100 || result["batch_truncated"] != true {
		t.Fatalf("unexpected batch result: %#v", result)
	}
	files := result["files"].([]map[string]any)
	for index, file := range files {
		content := file["content"].(string)
		if len(content) > 50 {
			t.Fatalf("file %d returned %d chars, want <= 50", index, len(content))
		}
		if file["batch_truncated"] != true {
			t.Fatalf("file %d missing batch truncation marker: %#v", index, file)
		}
	}
}

func BenchmarkReadTextSelectionEarlyStop(b *testing.B) {
	data := strings.Repeat("benchmark line\n", 1_000_000)
	b.ReportAllocs()
	for range b.N {
		_, _, _, _, _, err := readTextSelection(strings.NewReader(data), 10, 20, 16_000)
		if err != nil {
			b.Fatal(err)
		}
	}
}
