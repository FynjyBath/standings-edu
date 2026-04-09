package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

func EnsureDataBootstrap(dataDir string, informaticsCredsPath string) ([]string, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir %q: %w", dataDir, err)
	}

	created := make([]string, 0)

	if err := os.MkdirAll(filepath.Join(dataDir, "groups"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir groups dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(informaticsCredsPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir informatics creds dir: %w", err)
	}

	items := []bootstrapItem{
		{
			TargetPath:  filepath.Join(dataDir, "students.json"),
			ExamplePath: filepath.Join(dataDir, "students_example.json"),
			DefaultJSON: []byte("[]\n"),
		},
		{
			TargetPath:  filepath.Join(dataDir, "contests.json"),
			ExamplePath: filepath.Join(dataDir, "contests_example.json"),
			DefaultJSON: []byte("[]\n"),
		},
		{
			TargetPath:  filepath.Join(dataDir, "student_intake.json"),
			DefaultJSON: []byte("[]\n"),
		},
		{
			TargetPath:  filepath.Join(dataDir, "groups", "group_example", "group.json"),
			ExamplePath: filepath.Join(dataDir, "groups", "group_example", "group_example.json"),
		},
		{
			TargetPath:  filepath.Join(dataDir, "groups", "group_example", "contests.json"),
			ExamplePath: filepath.Join(dataDir, "groups", "group_example", "contests_example.json"),
		},
		{
			TargetPath:  informaticsCredsPath,
			ExamplePath: filepath.Join(filepath.Dir(informaticsCredsPath), "informatics_credentials_example.json"),
			DefaultJSON: []byte("{\n  \"username\": \"\",\n  \"password\": \"\",\n  \"base_url\": \"https://informatics.msk.ru\"\n}\n"),
		},
	}

	for _, item := range items {
		createdNow, err := ensureFile(item)
		if err != nil {
			return nil, err
		}
		if createdNow {
			created = append(created, item.TargetPath)
		}
	}

	return created, nil
}

type bootstrapItem struct {
	TargetPath  string
	ExamplePath string
	DefaultJSON []byte
}

func ensureFile(item bootstrapItem) (bool, error) {
	if _, err := os.Stat(item.TargetPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %q: %w", item.TargetPath, err)
	}

	var content []byte
	if item.ExamplePath != "" {
		b, err := os.ReadFile(item.ExamplePath)
		if err == nil {
			content = b
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("read example file %q: %w", item.ExamplePath, err)
		}
	}
	if len(content) == 0 && len(item.DefaultJSON) > 0 {
		content = item.DefaultJSON
	}
	if len(content) == 0 {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(item.TargetPath), 0o755); err != nil {
		return false, fmt.Errorf("mkdir for %q: %w", item.TargetPath, err)
	}
	if err := os.WriteFile(item.TargetPath, content, 0o644); err != nil {
		return false, fmt.Errorf("write file %q: %w", item.TargetPath, err)
	}

	return true, nil
}
