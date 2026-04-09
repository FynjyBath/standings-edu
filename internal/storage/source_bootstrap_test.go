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
	if len(created) != 4 {
		t.Fatalf("len(created) = %d, want 4", len(created))
	}

	cases := []struct {
		path string
		want string
	}{
		{path: filepath.Join(dataDir, "students.json"), want: "[]\n"},
		{path: filepath.Join(dataDir, "contests.json"), want: "[]\n"},
		{path: filepath.Join(dataDir, "groups", "group_example", "contests.json"), want: "[]\n"},
		{path: filepath.Join(dataDir, "groups", "group_example", "groups.json"), want: "{}\n"},
	}

	for _, tc := range cases {
		path := tc.path
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %q: %v", path, readErr)
		}
		if string(b) != tc.want {
			t.Fatalf("%q content = %q, want %q", path, string(b), tc.want)
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
