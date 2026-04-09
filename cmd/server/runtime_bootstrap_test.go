package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureServerRuntimeLayoutCreatesMissingRuntimePaths(t *testing.T) {
	base := t.TempDir()
	templatesDir := filepath.Join(base, "web", "templates")
	staticDir := filepath.Join(base, "web", "static")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("mkdir static: %v", err)
	}

	layout := serverRuntimeLayout{
		GeneratedDir: filepath.Join(base, "generated"),
		DataDir:      filepath.Join(base, "data"),
		IntakePath:   filepath.Join(base, "data", "student_intake.json"),
		TemplatesDir: templatesDir,
		StaticDir:    staticDir,
	}
	logger := log.New(io.Discard, "", 0)

	if err := ensureServerRuntimeLayout(layout, logger); err != nil {
		t.Fatalf("ensure runtime layout: %v", err)
	}

	assertDirExists(t, layout.GeneratedDir)
	assertDirExists(t, filepath.Join(layout.GeneratedDir, "standings"))
	assertDirExists(t, layout.DataDir)
	assertDirExists(t, filepath.Join(layout.DataDir, "groups"))
	assertDirExists(t, filepath.Join(layout.DataDir, "sites"))

	b, err := os.ReadFile(layout.IntakePath)
	if err != nil {
		t.Fatalf("read intake file: %v", err)
	}
	if string(b) != "[]\n" {
		t.Fatalf("unexpected intake file content: %q", string(b))
	}
}

func TestEnsureServerRuntimeLayoutDoesNotOverwriteIntakeFile(t *testing.T) {
	base := t.TempDir()
	templatesDir := filepath.Join(base, "web", "templates")
	staticDir := filepath.Join(base, "web", "static")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("mkdir static: %v", err)
	}

	intakePath := filepath.Join(base, "data", "student_intake.json")
	if err := os.MkdirAll(filepath.Dir(intakePath), 0o755); err != nil {
		t.Fatalf("mkdir intake dir: %v", err)
	}
	const original = "[{\"full_name\":\"Иванов Иван\"}]\n"
	if err := os.WriteFile(intakePath, []byte(original), 0o644); err != nil {
		t.Fatalf("write intake file: %v", err)
	}

	layout := serverRuntimeLayout{
		GeneratedDir: filepath.Join(base, "generated"),
		DataDir:      filepath.Join(base, "data"),
		IntakePath:   intakePath,
		TemplatesDir: templatesDir,
		StaticDir:    staticDir,
	}
	logger := log.New(io.Discard, "", 0)

	if err := ensureServerRuntimeLayout(layout, logger); err != nil {
		t.Fatalf("ensure runtime layout: %v", err)
	}

	b, err := os.ReadFile(intakePath)
	if err != nil {
		t.Fatalf("read intake file: %v", err)
	}
	if string(b) != original {
		t.Fatalf("intake file was unexpectedly overwritten: got %q", string(b))
	}
}

func TestEnsureServerRuntimeLayoutFailsOnWrongPathTypes(t *testing.T) {
	base := t.TempDir()
	templatesDir := filepath.Join(base, "web", "templates")
	staticDir := filepath.Join(base, "web", "static")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("mkdir static: %v", err)
	}

	generatedPath := filepath.Join(base, "generated")
	if err := os.WriteFile(generatedPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write generated file: %v", err)
	}

	layout := serverRuntimeLayout{
		GeneratedDir: generatedPath,
		DataDir:      filepath.Join(base, "data"),
		IntakePath:   filepath.Join(base, "data", "student_intake.json"),
		TemplatesDir: templatesDir,
		StaticDir:    staticDir,
	}
	logger := log.New(io.Discard, "", 0)

	err := ensureServerRuntimeLayout(layout, logger)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "generated dir") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertDirExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("path %q is not a directory", path)
	}
}
