package studentintake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"standings-edu/internal/domain"
)

var ErrMissingFullName = errors.New("full_name is required")
var ErrInvalidGroupSlug = errors.New("invalid group slug")

type Store struct {
	path    string
	dataDir string
	mu      sync.Mutex
}

func NewStore(path string, dataDir ...string) *Store {
	dir := filepath.Dir(path)
	if len(dataDir) > 0 && strings.TrimSpace(dataDir[0]) != "" {
		dir = strings.TrimSpace(dataDir[0])
	}
	return &Store{
		path:    path,
		dataDir: dir,
	}
}

func (s *Store) Submit(fields map[string]string) (domain.Student, error) {
	fullName := normalizeWhitespace(fields["full_name"])
	if fullName == "" {
		return domain.Student{}, ErrMissingFullName
	}
	groupSlug := strings.TrimSpace(fields["group"])

	s.mu.Lock()
	defer s.mu.Unlock()

	students, err := LoadStudentsFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return domain.Student{}, fmt.Errorf("load intake file: %w", err)
		}
		students = nil
	}

	idx := findStudentIndexByFullName(students, fullName)
	var student domain.Student

	if idx >= 0 {
		updated := students[idx]
		updated.FullName = fullName

		if publicName := normalizeWhitespace(fields["public_name"]); publicName != "" {
			updated.PublicName = publicName
		} else if strings.TrimSpace(updated.PublicName) == "" {
			updated.PublicName = GeneratePublicNameFromFullName(updated.FullName)
		}
		updated.Accounts = mergeAccountUpdates(updated.Accounts, accountsFromFields(fields))
		if updated.ID == "" || idTakenByOther(students, idx, updated.ID) {
			updated.ID = nextUniqueID(students, updated.FullName, idx)
		}
		if groupSlug != "" {
			updated.Groups = appendUnique(updated.Groups, groupSlug)
		}

		updated = normalizeStudent(updated)
		students[idx] = updated
		student = updated
	} else {
		student = domain.Student{
			ID:         nextUniqueID(students, fullName, -1),
			FullName:   fullName,
			PublicName: normalizeWhitespace(fields["public_name"]),
			Accounts:   accountsFromFields(fields),
		}
		if strings.TrimSpace(student.PublicName) == "" {
			student.PublicName = GeneratePublicNameFromFullName(student.FullName)
		}
		if groupSlug != "" {
			student.Groups = appendUnique(student.Groups, groupSlug)
		}
		student = normalizeStudent(student)

		students = append(students, student)
	}

	if groupSlug != "" {
		if err := s.syncGroupMembership(student, fields, groupSlug); err != nil {
			return domain.Student{}, err
		}
	}
	if err := WriteStudentsFile(s.path, students); err != nil {
		return domain.Student{}, fmt.Errorf("write intake file: %w", err)
	}
	return student, nil
}
