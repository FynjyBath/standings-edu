package web

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

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
			"statusSymbol":            statusSymbol,
			"statusClass":             statusClass,
			"scoreText":               scoreText,
			"scoreAlpha":              scoreAlpha,
			"placeText":               placeText,
			"penaltyText":             penaltyText,
			"hasPenaltyColumn":        hasPenaltyColumn,
			"hasProviderStatusColumn": hasProviderStatusColumn,
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

func scoreText(score *int) string {
	if score == nil {
		return ""
	}
	return fmt.Sprintf("%d", *score)
}

func scoreAlpha(score *int) string {
	if score == nil {
		return "0"
	}
	v := *score
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	alpha := float64(v) / 100.0
	return strconv.FormatFloat(alpha, 'f', 2, 64)
}

func placeText(place string) string {
	return place
}

func penaltyText(penalty *int) string {
	if penalty == nil {
		return ""
	}
	return fmt.Sprintf("%d", *penalty)
}

func hasPenaltyColumn(rows []domain.GeneratedRow) bool {
	for _, row := range rows {
		if row.Penalty != nil {
			return true
		}
	}
	return false
}

func hasProviderStatusColumn(rows []domain.GeneratedRow) bool {
	for _, row := range rows {
		if strings.TrimSpace(row.ProviderStatus) != "" {
			return true
		}
	}
	return false
}
