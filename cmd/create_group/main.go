package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"standings-edu/internal/domain"
)

func main() {
	var (
		dataDir  = flag.String("data-dir", "./data", "path to source data directory")
		slug     = flag.String("slug", "", "group slug (directory name)")
		slag     = flag.String("slag", "", "deprecated alias for -slug")
		name     = flag.String("name", "", "group title")
		formLink = flag.String("form-link", "", "URL for the account intake form")
	)
	flag.Parse()
	if *slug == "" && *slag != "" {
		*slug = *slag
	}

	if err := createEmptyGroup(*dataDir, *slug, *name, *formLink); err != nil {
		log.Fatalf("create group failed: %v", err)
	}

	log.Printf("group created: slug=%s", strings.TrimSpace(*slug))
}

func createEmptyGroup(dataDir, slug, name, formLink string) error {
	dataDir = strings.TrimSpace(dataDir)
	slug = strings.TrimSpace(slug)
	name = strings.TrimSpace(name)
	formLink = strings.TrimSpace(formLink)

	if !domain.IsValidSlug(slug) {
		return fmt.Errorf("invalid slug %q", slug)
	}
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if formLink == "" {
		return fmt.Errorf("form-link is required")
	}

	groupDir := filepath.Join(dataDir, "groups", slug)
	groupPath := filepath.Join(groupDir, "group.json")
	contestsPath := filepath.Join(groupDir, "contests.json")

	if fileExists(groupPath) || fileExists(contestsPath) {
		return fmt.Errorf("group %q already exists", slug)
	}

	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return fmt.Errorf("mkdir group dir %q: %w", groupDir, err)
	}

	groupFile := domain.GroupFile{
		Title:      name,
		FormLink:   formLink,
		Update:     boolPtr(true),
		StudentIDs: []string{},
	}
	if err := writeJSONFile(groupPath, groupFile); err != nil {
		return fmt.Errorf("write %q: %w", groupPath, err)
	}
	if err := writeJSONFile(contestsPath, []any{}); err != nil {
		return fmt.Errorf("write %q: %w", contestsPath, err)
	}

	return nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func boolPtr(v bool) *bool {
	return &v
}
