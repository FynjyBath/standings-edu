package web

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"standings-edu/internal/domain"
)

type TemplateRenderer struct {
	templatesDir string
	funcMap      template.FuncMap
}

func NewTemplateRenderer(templatesDir string) *TemplateRenderer {
	return &TemplateRenderer{
		templatesDir: templatesDir,
		funcMap: template.FuncMap{
			"statusSymbol": statusSymbol,
			"statusClass":  statusClass,
			"siteTitle":    siteTitle,
		},
	}
}

func (r *TemplateRenderer) Render(w http.ResponseWriter, statusCode int, pageTemplate string, data any) error {
	tmpl, err := template.New("layout.html").Funcs(r.funcMap).ParseFiles(
		filepath.Join(r.templatesDir, "layout.html"),
		filepath.Join(r.templatesDir, pageTemplate),
	)
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		return fmt.Errorf("render template: %w", err)
	}
	return nil
}

func statusSymbol(status string) string {
	switch status {
	case domain.TaskStatusSolved:
		return "+"
	case domain.TaskStatusAttempted:
		return "×"
	default:
		return ""
	}
}

func statusClass(status string) string {
	switch status {
	case domain.TaskStatusSolved:
		return "solved"
	case domain.TaskStatusAttempted:
		return "attempted"
	default:
		return "none"
	}
}

func siteTitle(site string) string {
	switch site {
	case "codeforces":
		return "Codeforces"
	case "informatics":
		return "Informatics"
	case "acmp":
		return "ACMP"
	default:
		return site
	}
}
