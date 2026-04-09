package studentintake

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"standings-edu/internal/domain"
)

func LoadStudentsFile(path string) ([]domain.Student, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}

	var students []domain.Student
	if err := json.Unmarshal(b, &students); err != nil {
		return nil, fmt.Errorf("decode json %q: %w", path, err)
	}
	return students, nil
}

func LoadIntakeFile(path string) ([]domain.Student, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("decode json %q: %w", path, err)
	}

	students := make([]domain.Student, 0, len(items))
	for i, item := range items {
		student, err := decodeIntakeItem(item)
		if err != nil {
			return nil, fmt.Errorf("decode intake item #%d in %q: %w", i, path, err)
		}
		if student.FullName == "" {
			return nil, fmt.Errorf("intake item #%d in %q has empty full_name", i, path)
		}
		students = append(students, student)
	}
	return students, nil
}

func WriteStudentsFile(path string, students []domain.Student) error {
	items := make([]studentJSON, 0, len(students))
	for _, s := range students {
		s = normalizeStudent(s)

		item := studentJSON{
			ID:       s.ID,
			FullName: s.FullName,
		}
		if s.PublicName != "" {
			item.PublicName = s.PublicName
		}
		if len(s.Accounts) > 0 {
			item.Accounts = s.Accounts
		}
		items = append(items, item)
	}

	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json %q: %w", path, err)
	}
	b = append(b, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}
	return nil
}

type studentJSON struct {
	ID         string           `json:"id"`
	FullName   string           `json:"full_name"`
	PublicName string           `json:"public_name,omitempty"`
	Accounts   []domain.Account `json:"accounts,omitempty"`
}

func decodeIntakeItem(item map[string]json.RawMessage) (domain.Student, error) {
	var student domain.Student

	if raw, ok := item["id"]; ok {
		if err := json.Unmarshal(raw, &student.ID); err != nil {
			return domain.Student{}, fmt.Errorf("field id: %w", err)
		}
	}
	if raw, ok := item["full_name"]; ok {
		if err := json.Unmarshal(raw, &student.FullName); err != nil {
			return domain.Student{}, fmt.Errorf("field full_name: %w", err)
		}
	}
	if raw, ok := item["public_name"]; ok {
		if err := json.Unmarshal(raw, &student.PublicName); err != nil {
			return domain.Student{}, fmt.Errorf("field public_name: %w", err)
		}
	}
	if raw, ok := item["accounts"]; ok {
		if err := json.Unmarshal(raw, &student.Accounts); err != nil {
			return domain.Student{}, fmt.Errorf("field accounts: %w", err)
		}
	}

	extraAccounts := make([]domain.Account, 0)
	extraKeys := make([]string, 0, len(item))
	for key := range item {
		extraKeys = append(extraKeys, key)
	}
	sort.Strings(extraKeys)

	for _, key := range extraKeys {
		raw := item[key]
		field := strings.TrimSpace(strings.ToLower(key))
		if field == "" || field == "id" || field == "full_name" || field == "public_name" || field == "accounts" {
			continue
		}

		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return domain.Student{}, fmt.Errorf("field %q: expected string value", key)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		extraAccounts = append(extraAccounts, domain.Account{
			Site:      field,
			AccountID: value,
		})
	}

	student = normalizeStudent(student)
	if len(extraAccounts) > 0 {
		student.Accounts = mergeAccountUpdates(student.Accounts, extraAccounts)
	}
	student.Accounts = normalizeAccounts(student.Accounts)

	return student, nil
}
