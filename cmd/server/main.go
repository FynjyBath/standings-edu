package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"standings-edu/internal/httpapi"
	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

func main() {
	var (
		addr         = flag.String("addr", ":8080", "HTTP listen address")
		generatedDir = flag.String("generated", "./generated", "path to generated files")
		templatesDir = flag.String("templates", "./web/templates", "path to templates")
		staticDir    = flag.String("static", "./web/static", "path to static files")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	loader := storage.NewGeneratedLoader(*generatedDir)
	renderer := web.NewTemplateRenderer(*templatesDir)
	handlers := httpapi.NewHandlers(loader, renderer, logger)
	router := httpapi.NewRouter(handlers, *staticDir)

	logger.Printf("server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, router); err != nil {
		logger.Fatalf("server stopped: %v", err)
	}
}
