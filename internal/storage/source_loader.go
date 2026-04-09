package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"standings-edu/internal/domain"
)

type SourceLoader struct {
	DataDir string
}

func NewSourceLoader(dataDir string) *SourceLoader {
	return &SourceLoader{DataDir: dataDir}
}

func (l *SourceLoader) Load() (*domain.SourceData, error) {
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
	for i, s := range students {
		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" {
			continue
		}
		s.FullName = strings.TrimSpace(s.FullName)
		if s.FullName == "" {
			return nil, fmt.Errorf("student item #%d has empty full_name", i)
		}
		s.PublicName = strings.TrimSpace(s.PublicName)
		if s.PublicName == "" {
			s.PublicName = s.FullName
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

		contests, err := l.loadGroupContests(filepath.Join(dir, "contests.json"))
		if err != nil {
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
			Contests:   contests,
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Slug < groups[j].Slug
	})

	return groups, nil
}

type groupContestJSON struct {
	ID     string `json:"id"`
	Update *bool  `json:"update,omitempty"`
}

func (l *SourceLoader) loadGroupContests(path string) ([]domain.GroupContestRef, error) {
	var items []groupContestJSON
	if err := readJSON(path, &items); err != nil {
		return nil, err
	}

	out := make([]domain.GroupContestRef, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return nil, fmt.Errorf("contest item #%d in %q has empty id", i, path)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("contest item #%d in %q duplicates id %q", i, path, id)
		}
		seen[id] = struct{}{}

		update := true
		if item.Update != nil {
			update = *item.Update
		}

		out = append(out, domain.GroupContestRef{
			ID:     id,
			Update: update,
		})
	}

	return out, nil
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
