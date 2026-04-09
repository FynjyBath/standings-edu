package studentintake

import (
	"fmt"
	"strings"

	"standings-edu/internal/domain"
)

type MergeStats struct {
	Updated int
	Added   int
}

func MergeStudents(existing []domain.Student, intake []domain.Student) ([]domain.Student, MergeStats, error) {
	result := make([]domain.Student, len(existing))
	for i, s := range existing {
		result[i] = normalizeStudent(s)
	}

	stats := MergeStats{}

	for i, incoming := range intake {
		incoming = normalizeStudent(incoming)
		if incoming.FullName == "" {
			return nil, MergeStats{}, fmt.Errorf("intake item #%d has empty full_name", i)
		}

		idx := findStudentIndexByFullName(result, incoming.FullName)
		if idx < 0 && incoming.ID != "" {
			idx = findStudentIndexByID(result, incoming.ID)
		}

		if idx >= 0 {
			merged := result[idx]
			merged = mergeExistingStudent(merged, incoming)
			merged.ID = strings.TrimSpace(result[idx].ID)
			if merged.ID == "" {
				merged.ID = nextUniqueID(result, merged.FullName, idx)
			}

			result[idx] = normalizeStudent(merged)
			stats.Updated++
			continue
		}

		newStudent := buildNewStudent(result, incoming)
		result = append(result, newStudent)
		stats.Added++
	}

	return result, stats, nil
}

func mergeExistingStudent(existing domain.Student, incoming domain.Student) domain.Student {
	if incoming.FullName != "" {
		existing.FullName = incoming.FullName
	}
	if incoming.PublicName != "" {
		existing.PublicName = incoming.PublicName
	} else if strings.TrimSpace(existing.PublicName) == "" {
		existing.PublicName = GeneratePublicNameFromFullName(existing.FullName)
	}
	existing.Accounts = mergeAccountUpdates(existing.Accounts, incoming.Accounts)
	return existing
}

func buildNewStudent(current []domain.Student, incoming domain.Student) domain.Student {
	id := slugifyASCII(incoming.ID)
	if id == "" || idTakenByOther(current, -1, id) {
		id = nextUniqueID(current, incoming.FullName, -1)
	}

	publicName := incoming.PublicName
	if strings.TrimSpace(publicName) == "" {
		publicName = GeneratePublicNameFromFullName(incoming.FullName)
	}

	return normalizeStudent(domain.Student{
		ID:         id,
		FullName:   incoming.FullName,
		PublicName: publicName,
		Accounts:   incoming.Accounts,
	})
}

func mergeAccountUpdates(existing []domain.Account, updates []domain.Account) []domain.Account {
	if len(updates) == 0 {
		return normalizeAccounts(existing)
	}

	merged := make([]domain.Account, 0, len(existing)+len(updates))
	indexBySite := make(map[string]int, len(existing))

	for _, acc := range normalizeAccounts(existing) {
		indexBySite[acc.Site] = len(merged)
		merged = append(merged, acc)
	}

	for _, update := range normalizeAccounts(updates) {
		if idx, ok := indexBySite[update.Site]; ok {
			merged[idx].AccountID = update.AccountID
			continue
		}
		indexBySite[update.Site] = len(merged)
		merged = append(merged, update)
	}

	return merged
}

func findStudentIndexByFullName(students []domain.Student, fullName string) int {
	for i := range students {
		if students[i].FullName == fullName {
			return i
		}
	}
	return -1
}

func findStudentIndexByID(students []domain.Student, id string) int {
	id = strings.TrimSpace(id)
	for i := range students {
		if strings.TrimSpace(students[i].ID) == id {
			return i
		}
	}
	return -1
}

func nextUniqueID(students []domain.Student, fullName string, currentIdx int) string {
	return GenerateUniqueID(fullName, func(id string) bool {
		return idTakenByOther(students, currentIdx, id)
	})
}

func idTakenByOther(students []domain.Student, currentIdx int, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}

	for i := range students {
		if i == currentIdx {
			continue
		}
		if strings.TrimSpace(students[i].ID) == id {
			return true
		}
	}
	return false
}
