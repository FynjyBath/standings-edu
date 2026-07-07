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
			"contestNotStarted":       contestNotStarted,
			"contestWindowText":       contestWindowText,
			"gradeText":               gradeText,
			"grade2Text":              grade2Text,
			"numText":                 numText,
			"submissionTime":          submissionTime,
			"siteName":                siteName,
			"dayLabel":                dayLabel,
			"sub":                     func(a, b int) int { return a - b },
			"barHeight":               barHeight,
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
		// Ошибка парсинга (например, шаблоны новее бинарника и используют
		// незнакомую функцию) — отвечаем явной 500, а не пустой страницей.
		http.Error(w, "internal error: template parse failed", http.StatusInternalServerError)
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

// taskCells объединяет статусы, баллы и пометку дорешки в единый набор ячеек.
// Для IOI основной балл и дорешка показываются вместе: «50 (70)» — 50 в окне,
// 70 в дорешке; только дорешка — «(70)». Фон ячейки — по лучшему из них.
func taskCells(row domain.GeneratedRow, scoreSystem domain.ScoreSystem) []TaskCell {
	isIOI := scoreSystem.IsIOI()
	cells := make([]TaskCell, 0, len(row.Statuses))
	for i := range row.Statuses {
		practice := i < len(row.Upsolved) && row.Upsolved[i]
		cell := TaskCell{IsIOI: isIOI, Practice: practice}

		if isIOI {
			var main, practiceScore *int
			if i < len(row.Scores) {
				main = row.Scores[i]
			}
			if i < len(row.PracticeScores) {
				practiceScore = row.PracticeScores[i]
			}
			effective := main
			if practiceScore != nil && (main == nil || *practiceScore > *main) {
				effective = practiceScore
			}
			cell.Alpha = scoreAlpha(effective)
			switch {
			case main != nil && practiceScore != nil:
				cell.Text = scoreText(main) + " (" + scoreText(practiceScore) + ")"
			case practiceScore != nil:
				cell.Text = "(" + scoreText(practiceScore) + ")"
			default:
				// Старые файлы без practice_scores: балл общий, дорешку
				// помечаем скобками по флагу, как раньше.
				cell.Text = wrapPractice(scoreText(main), practice)
			}
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

// grade2Text — оценка всегда с двумя знаками после запятой (для колонки точного
// итога). Пусто, если оценки нет.
func grade2Text(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', 2, 64)
}

// contestNotStarted — контест ещё не начался (StartTime в будущем): сервер уже
// спрятал ссылки на задачи, а шаблон показывает подсказку с временем старта.
func contestNotStarted(startTime *time.Time) bool {
	return startTime != nil && time.Now().Before(*startTime)
}

// contestWindowText — человекочитаемое окно контеста в MSK для шапки таблицы:
// «04.07.2026 18:00–20:00 MSK» (один день) или полный диапазон с датами.
// Только начало — «с 04.07.2026 18:00 MSK». Пусто — окна нет.
func contestWindowText(start, end *time.Time) string {
	if start == nil {
		return ""
	}
	s := start.In(moscowLocation)
	if end == nil {
		return "с " + s.Format("02.01.2006 15:04") + " MSK"
	}
	e := end.In(moscowLocation)
	if s.Year() == e.Year() && s.YearDay() == e.YearDay() {
		return s.Format("02.01.2006 15:04") + "–" + e.Format("15:04") + " MSK"
	}
	return s.Format("02.01.2006 15:04") + " — " + e.Format("02.01.2006 15:04") + " MSK"
}

// submissionTime форматирует время посылки в MSK для ленты профиля.
func submissionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(moscowLocation).Format("02.01.2006 15:04")
}

// siteName — человекочитаемое имя сайта для профиля участника.
func siteName(site string) string {
	switch site {
	case "codeforces":
		return "Codeforces"
	case "informatics":
		return "Informatics"
	case "acmp":
		return "ACMP"
	case "", "other":
		return "прочее"
	}
	return site
}

// barHeight — высота столбика графика активности в пикселях (3..60), масштаб
// от максимума за день. Ноль — маленький столбик-заглушка.
func barHeight(count, max int) int {
	if max <= 0 || count <= 0 {
		return 3
	}
	h := 3 + count*57/max
	if h > 60 {
		h = 60
	}
	return h
}

// dayLabel — короткая дата «дд.мм» из строки YYYY-MM-DD для подписи графика.
func dayLabel(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format("02.01")
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
