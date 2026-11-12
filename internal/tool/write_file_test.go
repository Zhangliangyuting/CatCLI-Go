package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFileRetrySkipsMatchingContent(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		name := "create"
		if overwrite {
			name = "overwrite"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			args := map[string]any{"file_path": path, "content": "target"}
			if overwrite {
				if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
				args["overwrite"] = true
				args["expected_hash"] = contentHash([]byte("old"))
			}
			if _, err := WriteFileHandler(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(1000000000, 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			result, err := WriteFileHandler(t.Context(), args)
			if err != nil || !strings.Contains(result, "no write performed") {
				t.Fatalf("retry: %q %v", result, err)
			}
			info, err := os.Stat(path)
			if err != nil || !info.ModTime().Equal(stamp) {
				t.Fatalf("matching file rewritten: %v %v", info, err)
			}
			if err := os.WriteFile(path, []byte("external change"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := WriteFileHandler(t.Context(), args); err == nil {
				t.Fatal("conflicting content overwritten")
			}
			if data, _ := os.ReadFile(path); string(data) != "external change" {
				t.Fatalf("conflicting content changed: %q", data)
			}
		})
	}
}
