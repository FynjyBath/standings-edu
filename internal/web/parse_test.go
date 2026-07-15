package web

import (
	"html/template"
	"path/filepath"
	"strings"
	"testing"
)

// Каждый страничный шаблон должен парситься вместе с layout и partial-ами — тот
// же набор, что собирает Render. Ловит несбалансированные {{if}}/{{range}},
// опечатки в пайпах и неизвестные функции сразу по всем страницам.
func TestAllPageTemplatesParse(t *testing.T) {
	dir := "../../web/templates"
	r := NewTemplateRenderer(dir)

	layout := filepath.Join(dir, "layout.html")
	partials, _ := filepath.Glob(filepath.Join(dir, "*.partial.html"))
	pages, err := filepath.Glob(filepath.Join(dir, "*.html"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no templates found: %v", err)
	}

	for _, p := range pages {
		base := filepath.Base(p)
		if base == "layout.html" || strings.HasSuffix(base, ".partial.html") {
			continue
		}
		files := append([]string{layout}, partials...)
		files = append(files, p)
		if _, err := template.New("layout.html").Funcs(r.funcMap).ParseFiles(files...); err != nil {
			t.Errorf("%s: parse failed: %v", base, err)
		}
	}
}
