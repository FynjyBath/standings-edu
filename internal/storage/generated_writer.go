package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
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
	if err := fileutil.WriteJSON(path, groups, 0o644); err != nil {
		return fmt.Errorf("write groups %q: %w", path, err)
	}
	return nil
}

func (w *GeneratedWriter) WriteGroupStandings(standings domain.GeneratedGroupStandings) error {
	standingsDir := filepath.Join(w.OutDir, "standings")
	if err := os.MkdirAll(standingsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir standings dir: %w", err)
	}

	path := filepath.Join(standingsDir, standings.GroupSlug+".json")
	if err := fileutil.WriteJSON(path, standings, 0o644); err != nil {
		return fmt.Errorf("write standings %q: %w", path, err)
	}
	return nil
}
