package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"standings-edu/internal/domain"
)

type SourceLoader struct {
	DataDir string
}

func NewSourceLoader(dataDir string) *SourceLoader {
	return &SourceLoader{DataDir: dataDir}
}

func (l *SourceLoader) Load(ctx context.Context) (*domain.SourceData, error) {
	_ = ctx

	students, err := l.loadStudents()
	if err != nil {
		return nil, err
	}
	contests, err := l.loadContests()
	if err != nil {
		return nil, err
	}
	groups, err := l.loadGroups()
	if err != nil {
		return nil, err
	}

	return &domain.SourceData{
		Students: students,
		Contests: contests,
		Groups:   groups,
	}, nil
}

func (l *SourceLoader) loadStudents() (map[string]domain.Student, error) {
	path := filepath.Join(l.DataDir, "students.json")
	var students []domain.Student
	if err := readJSON(path, &students); err != nil {
		return nil, fmt.Errorf("load students: %w", err)
	}

	out := make(map[string]domain.Student, len(students))
	for _, s := range students {
		if s.ID == "" {
			continue
		}
		out[s.ID] = s
	}
	return out, nil
}

func (l *SourceLoader) loadContests() (map[string]domain.Contest, error) {
	path := filepath.Join(l.DataDir, "contests.json")
	var contests []domain.Contest
	if err := readJSON(path, &contests); err != nil {
		return nil, fmt.Errorf("load contests: %w", err)
	}

	out := make(map[string]domain.Contest, len(contests))
	for _, c := range contests {
		if c.ID == "" {
			continue
		}
		out[c.ID] = c
	}
	return out, nil
}

func (l *SourceLoader) loadGroups() ([]domain.GroupDefinition, error) {
	groupsDir := filepath.Join(l.DataDir, "groups")
	entries, err := os.ReadDir(groupsDir)
	if err != nil {
		return nil, fmt.Errorf("read groups dir %q: %w", groupsDir, err)
	}

	groups := make([]domain.GroupDefinition, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		dir := filepath.Join(groupsDir, slug)

		var gf domain.GroupFile
		if err := readJSON(filepath.Join(dir, "group.json"), &gf); err != nil {
			return nil, fmt.Errorf("load group %q: %w", slug, err)
		}

		var contestIDs []string
		if err := readJSON(filepath.Join(dir, "contests.json"), &contestIDs); err != nil {
			return nil, fmt.Errorf("load group contests %q: %w", slug, err)
		}

		update := true
		if gf.Update != nil {
			update = *gf.Update
		}

		groups = append(groups, domain.GroupDefinition{
			Slug:       slug,
			Title:      gf.Title,
			Update:     update,
			StudentIDs: gf.StudentIDs,
			ContestIDs: contestIDs,
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Slug < groups[j].Slug
	})

	return groups, nil
}

func readJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file %q: %w", path, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode json %q: %w", path, err)
	}
	return nil
}
