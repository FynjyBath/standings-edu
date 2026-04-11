package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
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
		Update:     pointerTo(true),
		StudentIDs: []string{},
	}
	if err := fileutil.WriteJSON(groupPath, groupFile, 0o644); err != nil {
		return fmt.Errorf("write %q: %w", groupPath, err)
	}
	if err := fileutil.WriteJSON(contestsPath, []any{}, 0o644); err != nil {
		return fmt.Errorf("write %q: %w", contestsPath, err)
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pointerTo(v bool) *bool {
	return &v
}
