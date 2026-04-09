package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureInitialSourceFiles(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	created, err := EnsureInitialSourceFiles(dataDir)
	if err != nil {
		t.Fatalf("EnsureInitialSourceFiles() error = %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("len(created) = %d, want 2", len(created))
	}

	for _, name := range []string{"students.json", "contests.json"} {
		path := filepath.Join(dataDir, name)
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %q: %v", path, readErr)
		}
		if string(b) != "[]\n" {
			t.Fatalf("%q content = %q, want %q", path, string(b), "[]\n")
		}
	}

	createdAgain, err := EnsureInitialSourceFiles(dataDir)
	if err != nil {
		t.Fatalf("EnsureInitialSourceFiles() second call error = %v", err)
	}
	if len(createdAgain) != 0 {
		t.Fatalf("len(createdAgain) = %d, want 0", len(createdAgain))
	}
}
