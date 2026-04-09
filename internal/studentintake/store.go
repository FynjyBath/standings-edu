package studentintake

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"standings-edu/internal/domain"
)

var ErrMissingFullName = errors.New("full_name is required")

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Submit(fields map[string]string) (domain.Student, error) {
	fullName := normalizeWhitespace(fields["full_name"])
	if fullName == "" {
		return domain.Student{}, ErrMissingFullName
	}

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

		updated = normalizeStudent(updated)
		students[idx] = updated

		if err := WriteStudentsFile(s.path, students); err != nil {
			return domain.Student{}, fmt.Errorf("write intake file: %w", err)
		}
		return updated, nil
	}

	student := domain.Student{
		ID:         nextUniqueID(students, fullName, -1),
		FullName:   fullName,
		PublicName: normalizeWhitespace(fields["public_name"]),
		Accounts:   accountsFromFields(fields),
	}
	if strings.TrimSpace(student.PublicName) == "" {
		student.PublicName = GeneratePublicNameFromFullName(student.FullName)
	}
	student = normalizeStudent(student)

	students = append(students, student)
	if err := WriteStudentsFile(s.path, students); err != nil {
		return domain.Student{}, fmt.Errorf("write intake file: %w", err)
	}
	return student, nil
}
