package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

type GeneratedLoader struct {
	OutDir string
}

var ErrInvalidGroupSlug = errors.New("invalid group slug")

func NewGeneratedLoader(outDir string) *GeneratedLoader {
	return &GeneratedLoader{OutDir: outDir}
}

func (l *GeneratedLoader) LoadGroups() ([]domain.GeneratedGroupMeta, error) {
	path := filepath.Join(l.OutDir, "groups.json")
	var groups []domain.GeneratedGroupMeta
	if err := fileutil.ReadJSON(path, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (l *GeneratedLoader) LoadGroupStandings(slug string) (domain.GeneratedGroupStandings, error) {
	if !domain.IsValidSlug(slug) {
		return domain.GeneratedGroupStandings{}, ErrInvalidGroupSlug
	}

	path := filepath.Join(l.OutDir, "standings", slug+".json")
	var standings domain.GeneratedGroupStandings
	if err := fileutil.ReadJSON(path, &standings); err != nil {
		return domain.GeneratedGroupStandings{}, err
	}
	return standings, nil
}

func (l *GeneratedLoader) LoadLastUpdatedAt() (time.Time, error) {
	candidates := []string{
		filepath.Join(l.OutDir, "groups.json"),
	}

	latest := time.Time{}
	found := false

	for _, p := range candidates {
		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return time.Time{}, err
		}
		if !found || info.ModTime().After(latest) {
			latest = info.ModTime()
			found = true
		}
	}

	standingsDir := filepath.Join(l.OutDir, "standings")
	entries, err := os.ReadDir(standingsDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return time.Time{}, err
		}
	} else {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
				continue
			}
			info, statErr := os.Stat(filepath.Join(standingsDir, e.Name()))
			if statErr != nil {
				continue
			}
			if !found || info.ModTime().After(latest) {
				latest = info.ModTime()
				found = true
			}
		}
	}

	if !found {
		return time.Time{}, os.ErrNotExist
	}
	return latest, nil
}
