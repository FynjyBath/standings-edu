package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

func EnsureInitialSourceFiles(dataDir string) ([]string, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir %q: %w", dataDir, err)
	}

	paths := []string{
		filepath.Join(dataDir, "students.json"),
		filepath.Join(dataDir, "contests.json"),
	}

	created := make([]string, 0, len(paths))
	for _, path := range paths {
		ok, err := ensureEmptyJSONArrayFile(path)
		if err != nil {
			return nil, err
		}
		if ok {
			created = append(created, path)
		}
	}
	return created, nil
}

func ensureEmptyJSONArrayFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("path %q is a directory", path)
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %q: %w", path, err)
	}

	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		return false, fmt.Errorf("write file %q: %w", path, err)
	}
	return true, nil
}
