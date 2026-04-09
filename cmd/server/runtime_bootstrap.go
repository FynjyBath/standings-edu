package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type serverRuntimeLayout struct {
	GeneratedDir string
	DataDir      string
	IntakePath   string
}

func ensureServerRuntimeLayout(layout serverRuntimeLayout, logger *log.Logger) error {
	if logger == nil {
		logger = log.Default()
	}

	if err := ensureDir(layout.GeneratedDir, true, logger); err != nil {
		return fmt.Errorf("generated dir: %w", err)
	}
	if err := ensureDir(filepath.Join(layout.GeneratedDir, "standings"), true, logger); err != nil {
		return fmt.Errorf("generated standings dir: %w", err)
	}
	if err := ensureDir(layout.DataDir, true, logger); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if err := ensureJSONFile(filepath.Join(layout.DataDir, "students.json"), []byte("[]\n"), logger); err != nil {
		return fmt.Errorf("students file: %w", err)
	}
	if err := ensureJSONFile(filepath.Join(layout.DataDir, "contests.json"), []byte("[]\n"), logger); err != nil {
		return fmt.Errorf("contests file: %w", err)
	}
	if err := ensureDir(filepath.Join(layout.DataDir, "groups"), true, logger); err != nil {
		return fmt.Errorf("data groups dir: %w", err)
	}
	if err := ensureDir(filepath.Join(layout.DataDir, "sites"), true, logger); err != nil {
		return fmt.Errorf("data sites dir: %w", err)
	}
	if err := ensureJSONFile(layout.IntakePath, []byte("[]\n"), logger); err != nil {
		return fmt.Errorf("intake file: %w", err)
	}
	return nil
}

func ensureDir(path string, create bool, logger *log.Logger) error {
	path = filepath.Clean(path)

	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("path %q is not a directory", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", path, err)
	}
	if !create {
		return fmt.Errorf("required directory %q does not exist", path)
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", path, err)
	}
	logger.Printf("bootstrap: created directory %s", path)
	return nil
}

func ensureJSONFile(path string, body []byte, logger *log.Logger) error {
	path = filepath.Clean(path)

	if err := ensureDir(filepath.Dir(path), true, logger); err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("path %q is a directory, expected file", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", path, err)
	}

	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}
	logger.Printf("bootstrap: created file %s", path)
	return nil
}
