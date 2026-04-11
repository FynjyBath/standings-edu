package fileutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func ReadJSON(path string, out any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file %q: %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode json %q: %w", path, err)
	}
	return nil
}

func MarshalJSON(value any) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func WriteJSON(path string, value any, mode os.FileMode) error {
	body, err := MarshalJSON(value)
	if err != nil {
		return fmt.Errorf("marshal json %q: %w", path, err)
	}
	if mode == 0 {
		mode = 0o644
	}
	return WriteFileAtomic(path, body, mode)
}

func DetectFileMode(path string, defaultMode os.FileMode) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().Perm(), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return defaultMode, nil
	}
	return 0, fmt.Errorf("stat file %q: %w", path, err)
}

func WriteFileAtomic(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmpFile.Write(body); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
