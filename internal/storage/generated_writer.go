package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"standings-edu/internal/domain"
)

type GeneratedWriter struct {
	OutDir string
}

func NewGeneratedWriter(outDir string) *GeneratedWriter {
	return &GeneratedWriter{OutDir: outDir}
}

func (w *GeneratedWriter) WriteGroups(groups []domain.GeneratedGroupMeta) error {
	if err := os.MkdirAll(w.OutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out dir: %w", err)
	}

	path := filepath.Join(w.OutDir, "groups.json")
	return writeJSON(path, groups)
}

func (w *GeneratedWriter) WriteGroupStandings(standings domain.GeneratedGroupStandings) error {
	standingsDir := filepath.Join(w.OutDir, "standings")
	if err := os.MkdirAll(standingsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir standings dir: %w", err)
	}

	path := filepath.Join(standingsDir, standings.GroupSlug+".json")
	return writeJSON(path, standings)
}

func (w *GeneratedWriter) WriteOverallStandings(standings domain.GeneratedOverallStandings) error {
	if err := os.MkdirAll(w.OutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out dir: %w", err)
	}

	path := filepath.Join(w.OutDir, "summary.json")
	return writeJSON(path, standings)
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json %q: %w", path, err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}
	return nil
}
