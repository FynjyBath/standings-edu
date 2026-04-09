package studentintake

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"standings-edu/internal/domain"
)

func (s *Store) syncGroupMembership(intakeStudent domain.Student, fields map[string]string, groupSlug string) error {
	groupSlug = strings.TrimSpace(groupSlug)
	if groupSlug == "" {
		return nil
	}

	groupPath, groupFile, err := loadGroupFile(s.dataDir, groupSlug)
	if err != nil {
		return fmt.Errorf("load group %q: %w", groupSlug, err)
	}

	studentsPath := filepath.Join(s.dataDir, "students.json")
	sourceStudents, err := LoadStudentsFile(studentsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load source students: %w", err)
		}
		sourceStudents = nil
	}

	idx := findStudentIndexByFullName(sourceStudents, intakeStudent.FullName)
	if idx < 0 && intakeStudent.ID != "" {
		idx = findStudentIndexByID(sourceStudents, intakeStudent.ID)
	}

	var sourceStudent domain.Student
	if idx >= 0 {
		sourceStudent = sourceStudents[idx]
		sourceStudent.FullName = intakeStudent.FullName

		if publicName := normalizeWhitespace(fields["public_name"]); publicName != "" {
			sourceStudent.PublicName = publicName
		} else if strings.TrimSpace(sourceStudent.PublicName) == "" {
			sourceStudent.PublicName = GeneratePublicNameFromFullName(sourceStudent.FullName)
		}

		sourceStudent.Accounts = mergeAccountUpdates(sourceStudent.Accounts, accountsFromFields(fields))
		sourceStudent.Groups = appendUnique(sourceStudent.Groups, groupSlug)

		if sourceStudent.ID == "" || idTakenByOther(sourceStudents, idx, sourceStudent.ID) {
			candidateID := strings.TrimSpace(intakeStudent.ID)
			if candidateID == "" || idTakenByOther(sourceStudents, idx, candidateID) {
				candidateID = nextUniqueID(sourceStudents, sourceStudent.FullName, idx)
			}
			sourceStudent.ID = candidateID
		}

		sourceStudent = normalizeStudent(sourceStudent)
		sourceStudents[idx] = sourceStudent
	} else {
		candidateID := strings.TrimSpace(intakeStudent.ID)
		if candidateID == "" || idTakenByOther(sourceStudents, -1, candidateID) {
			candidateID = nextUniqueID(sourceStudents, intakeStudent.FullName, -1)
		}

		sourceStudent = normalizeStudent(domain.Student{
			ID:         candidateID,
			FullName:   intakeStudent.FullName,
			PublicName: intakeStudent.PublicName,
			Accounts:   intakeStudent.Accounts,
			Groups:     appendUnique(nil, groupSlug),
		})
		if sourceStudent.PublicName == "" {
			sourceStudent.PublicName = GeneratePublicNameFromFullName(sourceStudent.FullName)
		}

		sourceStudents = append(sourceStudents, sourceStudent)
	}

	if err := WriteStudentsFile(studentsPath, sourceStudents); err != nil {
		return fmt.Errorf("write source students: %w", err)
	}

	groupFile.StudentIDs = appendUnique(groupFile.StudentIDs, sourceStudent.ID)
	if err := writeGroupFile(groupPath, groupFile); err != nil {
		return fmt.Errorf("write group file %q: %w", groupPath, err)
	}

	return nil
}

func loadGroupFile(dataDir, groupSlug string) (string, domain.GroupFile, error) {
	groupDir := filepath.Join(dataDir, "groups", groupSlug)
	paths := []string{
		filepath.Join(groupDir, "group.json"),
		filepath.Join(groupDir, "groups.json"),
	}

	for _, path := range paths {
		groupFile, err := readGroupFile(path)
		if err == nil {
			return path, groupFile, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return "", domain.GroupFile{}, err
	}

	return "", domain.GroupFile{}, os.ErrNotExist
}

func readGroupFile(path string) (domain.GroupFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return domain.GroupFile{}, err
	}

	var groupFile domain.GroupFile
	if err := json.Unmarshal(b, &groupFile); err != nil {
		return domain.GroupFile{}, fmt.Errorf("decode group file %q: %w", path, err)
	}
	return groupFile, nil
}

func writeGroupFile(path string, groupFile domain.GroupFile) error {
	groupFile.StudentIDs = normalizeGroups(groupFile.StudentIDs)

	b, err := json.MarshalIndent(groupFile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal group file %q: %w", path, err)
	}
	b = append(b, '\n')

	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	return nil
}
