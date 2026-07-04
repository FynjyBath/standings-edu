package web

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"standings-edu/internal/domain"
)

var moscowLocation = loadMoscowLocation()

func loadMoscowLocation() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*60*60)
	}
	return loc
}

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
			"taskCells":               taskCells,
			"contestGeneratedAt":      contestGeneratedAt,
			"gradeText":               gradeText,
			"numText":                 numText,
		},
	}
}

func (r *TemplateRenderer) Render(w http.ResponseWriter, statusCode int, pageTemplate string, data any) error {
	files := []string{filepath.Join(r.templatesDir, "layout.html")}
	// Общие partial-шаблоны (переиспользуемые блоки, напр. форма контеста).
	partials, _ := filepath.Glob(filepath.Join(r.templatesDir, "*.partial.html"))
	files = append(files, partials...)
	files = append(files, filepath.Join(r.templatesDir, pageTemplate))

	tmpl, err := template.New("layout.html").Funcs(r.funcMap).ParseFiles(files...)
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

// TaskCell — подготовленная к рендеру ячейка задачи в строке standings.
type TaskCell struct {
	IsIOI    bool
	Status   string // класс статуса (solved/attempted/none) для не-IOI
	Text     string // символ или баллы; для дорешки уже обёрнуто в скобки
	Alpha    string // прозрачность фона для IOI
	Practice bool   // дорешка: добавляет CSS-класс practice
}

// taskCells объединяет статусы, баллы и пометку дорешки в единый набор ячеек,
// чтобы шаблон одинаково рендерил IOI и обычный режим, оборачивая дорешку в скобки.
func taskCells(row domain.GeneratedRow, scoreSystem domain.ScoreSystem) []TaskCell {
	isIOI := scoreSystem.IsIOI()
	cells := make([]TaskCell, 0, len(row.Statuses))
	for i := range row.Statuses {
		practice := i < len(row.Upsolved) && row.Upsolved[i]
		cell := TaskCell{IsIOI: isIOI, Practice: practice}

		if isIOI {
			var score *int
			if i < len(row.Scores) {
				score = row.Scores[i]
			}
			cell.Text = wrapPractice(scoreText(score), practice)
			cell.Alpha = scoreAlpha(score)
		} else {
			cell.Status = statusClass(row.Statuses[i])
			cell.Text = wrapPractice(statusSymbol(row.Statuses[i]), practice)
		}
		cells = append(cells, cell)
	}
	return cells
}

func numText(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func gradeText(v *float64) string {
	if v == nil {
		return ""
	}
	return numText(*v)
}

func contestGeneratedAt(generatedAt *time.Time) string {
	if generatedAt == nil || generatedAt.IsZero() {
		return ""
	}
	return generatedAt.In(moscowLocation).Format("02.01.2006 15:04:05 MST")
}

func wrapPractice(text string, practice bool) string {
	if practice && text != "" {
		return "(" + text + ")"
	}
	return text
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
