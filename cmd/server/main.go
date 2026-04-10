package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"standings-edu/internal/httpapi"
	"standings-edu/internal/storage"
	"standings-edu/internal/studentintake"
	"standings-edu/internal/web"
)

func main() {
	var (
		addr         = flag.String("addr", ":8080", "HTTP listen address")
		generatedDir = flag.String("generated-dir", "./generated", "path to generated files")
		dataDir      = flag.String("data-dir", "./data", "path to source data directory")
		intakePath   = flag.String("intake-file", "", "path to intake json file (default: <data>/student_intake.json)")
		adminCreds   = flag.String("admin-creds-file", "./data/credentials/admin_credentials.json", "path to admin credentials JSON with login/password")
		templatesDir = flag.String("templates", "./web/templates", "path to templates")
		staticDir    = flag.String("static", "./web/static", "path to static files")
	)
	flag.Parse()

	if *intakePath == "" {
		*intakePath = filepath.Join(*dataDir, "student_intake.json")
	}
	studentsPath := filepath.Join(*dataDir, "students.json")
	contestsPath := filepath.Join(*dataDir, "contests.json")

	logger := log.New(os.Stdout, "", log.LstdFlags)

	projectRoot, err := os.Getwd()
	if err != nil {
		logger.Fatalf("resolve project root: %v", err)
	}
	projectRoot, err = filepath.Abs(projectRoot)
	if err != nil {
		logger.Fatalf("resolve absolute project root: %v", err)
	}

	if err := ensureServerRuntimeLayout(serverRuntimeLayout{
		GeneratedDir: *generatedDir,
		DataDir:      *dataDir,
		StudentsPath: studentsPath,
		ContestsPath: contestsPath,
		IntakePath:   *intakePath,
	}, logger); err != nil {
		logger.Fatalf("prepare server runtime layout: %v", err)
	}
	adminLogin, adminPassword, err := loadAdminCredentials(*adminCreds)
	if err != nil {
		logger.Fatalf("load admin credentials: %v", err)
	}

	loader := storage.NewGeneratedLoader(*generatedDir)
	intakeStore := studentintake.NewStore(*intakePath, *dataDir)
	renderer := web.NewTemplateRenderer(*templatesDir)
	handlers := httpapi.NewHandlers(loader, intakeStore, renderer, logger)
	if err := handlers.ConfigureAdmin(httpapi.AdminConfig{
		Login:        adminLogin,
		Password:     adminPassword,
		ProjectRoot:  projectRoot,
		DataDir:      *dataDir,
		GeneratedDir: *generatedDir,
	}); err != nil {
		logger.Fatalf("configure admin: %v", err)
	}
	router := httpapi.NewRouter(handlers, *staticDir)

	logger.Printf("server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, router); err != nil {
		logger.Fatalf("server stopped: %v", err)
	}
}

type adminCredentials struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

func loadAdminCredentials(path string) (string, string, error) {
	path = filepath.Clean(path)
	body, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %q: %w", path, err)
	}

	var cfg adminCredentials
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", "", fmt.Errorf("decode %q: %w", path, err)
	}
	cfg.Login = strings.TrimSpace(cfg.Login)
	cfg.Password = strings.TrimSpace(cfg.Password)
	if cfg.Login == "" || cfg.Password == "" {
		return "", "", fmt.Errorf("%q must contain non-empty fields: login, password", path)
	}

	return cfg.Login, cfg.Password, nil
}
