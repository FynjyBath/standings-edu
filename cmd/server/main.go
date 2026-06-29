package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
		intakeCreds  = flag.String("intake-creds-file", "./data/credentials/intake_credentials.json", "path to optional intake token JSON ({\"token\":\"...\"}) protecting POST /api/rpc")
		templatesDir = flag.String("templates", "./web/templates", "path to templates")
		staticDir    = flag.String("static", "./web/static", "path to static files")
	)
	flag.Parse()

	if *intakePath == "" {
		*intakePath = filepath.Join(*dataDir, "student_intake.json")
	}
	intakeAdminPath := filepath.Join(*dataDir, "student_intake_admin.json")
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
		GeneratedDir:    *generatedDir,
		DataDir:         *dataDir,
		StudentsPath:    studentsPath,
		ContestsPath:    contestsPath,
		IntakePath:      *intakePath,
		IntakeAdminPath: intakeAdminPath,
	}, logger); err != nil {
		logger.Fatalf("prepare server runtime layout: %v", err)
	}
	adminLogin, adminPassword, err := loadAdminCredentials(*adminCreds)
	if err != nil {
		logger.Fatalf("load admin credentials: %v", err)
	}
	intakeToken, err := loadIntakeToken(*intakeCreds)
	if err != nil {
		logger.Fatalf("load intake token: %v", err)
	}

	loader := storage.NewGeneratedLoader(*generatedDir)
	intakeStore := studentintake.NewStore(*intakePath)
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
	handlers.ConfigureSourceDir(*dataDir)
	handlers.ConfigureIntakeToken(intakeToken)
	if intakeToken == "" {
		logger.Printf("WARN intake token is not configured (%s); POST /api/rpc принимает анкеты без токена", *intakeCreds)
	} else {
		logger.Printf("intake token configured; POST /api/rpc требует токен")
	}
	router := httpapi.NewRouter(handlers, *staticDir)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Printf("server listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			logger.Fatalf("server stopped: %v", err)
		}
	case <-ctx.Done():
		logger.Printf("shutdown signal received, stopping server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Fatalf("graceful shutdown failed: %v", err)
		}
		logger.Printf("server stopped cleanly")
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

type intakeCredentials struct {
	Token string `json:"token"`
}

// loadIntakeToken читает секретный токен для POST /api/rpc.
// Файла нет/пустой → токен не задан (защита выключена); файл есть, но битый или
// без token → ошибка конфигурации.
func loadIntakeToken(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}

	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", nil
	}

	var cfg intakeCredentials
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", fmt.Errorf("decode %q: %w", path, err)
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return "", fmt.Errorf("%q must contain non-empty field: token", path)
	}
	return token, nil
}
