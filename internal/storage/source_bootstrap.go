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
	if err := os.MkdirAll(filepath.Join(dataDir, "groups", "group_example"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir group_example dir: %w", err)
	}

	items := []struct {
		path    string
		content []byte
	}{
		{
			path:    filepath.Join(dataDir, "students.json"),
			content: []byte("[]\n"),
		},
		{
			path:    filepath.Join(dataDir, "contests.json"),
			content: []byte("[]\n"),
		},
		{
			path:    filepath.Join(dataDir, "groups", "group_example", "contests.json"),
			content: []byte("[]\n"),
		},
		{
			path:    filepath.Join(dataDir, "groups", "group_example", "groups.json"),
			content: []byte("{}\n"),
		},
	}

	created := make([]string, 0, len(items))
	for _, item := range items {
		ok, err := ensureFileWithContent(item.path, item.content)
		if err != nil {
			return nil, err
		}
		if ok {
			created = append(created, item.path)
		}
	}
	return created, nil
}

func ensureFileWithContent(path string, content []byte) (bool, error) {
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

	if err := os.WriteFile(path, content, 0o644); err != nil {
		return false, fmt.Errorf("write file %q: %w", path, err)
	}
	return true, nil
}
