package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

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
		templatesDir = flag.String("templates", "./web/templates", "path to templates")
		staticDir    = flag.String("static", "./web/static", "path to static files")
	)
	flag.Parse()

	if *intakePath == "" {
		*intakePath = filepath.Join(*dataDir, "student_intake.json")
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	if err := ensureServerRuntimeLayout(serverRuntimeLayout{
		GeneratedDir: *generatedDir,
		DataDir:      *dataDir,
		IntakePath:   *intakePath,
	}, logger); err != nil {
		logger.Fatalf("prepare server runtime layout: %v", err)
	}

	loader := storage.NewGeneratedLoader(*generatedDir)
	intakeStore := studentintake.NewStore(*intakePath, *dataDir)
	renderer := web.NewTemplateRenderer(*templatesDir)
	handlers := httpapi.NewHandlers(loader, intakeStore, renderer, logger)
	router := httpapi.NewRouter(handlers, *staticDir)

	logger.Printf("server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, router); err != nil {
		logger.Fatalf("server stopped: %v", err)
	}
}
