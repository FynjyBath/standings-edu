package web

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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

	// Кэш распарсенных наборов шаблонов по имени страницы. Инвалидация — по
	// отпечатку mtime/size всех входящих файлов, поэтому правка шаблона
	// подхватывается без рестарта, как и раньше, но парсинг не повторяется на
	// каждый запрос.
	cacheMu sync.RWMutex
	cache   map[string]cachedTemplate
}

type cachedTemplate struct {
	fingerprint string
	tmpl        *template.Template
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
			"upsolvingView":           upsolvingView,
			"contestGeneratedAt":      contestGeneratedAt,
			"contestGeneratedAtISO":   contestGeneratedAtISO,
			"timeISO":                 timeISO,
			"gradeText":               gradeText,
			"grade2Text":              grade2Text,
			"numText":                 numText,
			"pct":                     func(v float64) int { return int(math.Round(v * 100)) },
			"submissionTime":          submissionTime,
			"submissionLink":          submissionLink,
			"siteName":                siteName,
			"dayLabel":                dayLabel,
			"sub":                     func(a, b int) int { return a - b },
			"barHeight":               barHeight,
			// dict собирает map для передачи нескольких значений в {{template}}
			// (контекст выносимых блоков, напр. таблицы контеста).
			"dict": func(kv ...any) map[string]any {
				m := make(map[string]any, len(kv)/2)
				for i := 0; i+1 < len(kv); i += 2 {
					if k, ok := kv[i].(string); ok {
						m[k] = kv[i+1]
					}
				}
				return m
			},
		},
	}
}

func (r *TemplateRenderer) Render(w http.ResponseWriter, statusCode int, pageTemplate string, data any) error {
	tmpl, err := r.load(pageTemplate)
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

// RenderFragment исполняет отдельный define из набора страницы pageTemplate без
// layout-обёртки (для отдаваемых по частям кусков страницы, напр. таблицы
// контеста при ленивой подгрузке).
func (r *TemplateRenderer) RenderFragment(w http.ResponseWriter, statusCode int, pageTemplate, defineName string, data any) error {
	tmpl, err := r.load(pageTemplate)
	if err != nil {
		http.Error(w, "internal error: template parse failed", http.StatusInternalServerError)
		return fmt.Errorf("parse templates: %w", err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	if err := tmpl.ExecuteTemplate(w, defineName, data); err != nil {
		return fmt.Errorf("render fragment %s: %w", defineName, err)
	}
	return nil
}

// load возвращает распарсенный набор шаблонов страницы из кэша; отпечаток по
// mtime/size файлов, поэтому изменения на диске подхватываются автоматически.
func (r *TemplateRenderer) load(pageTemplate string) (*template.Template, error) {
	files := []string{filepath.Join(r.templatesDir, "layout.html")}
	// Общие partial-шаблоны (переиспользуемые блоки, напр. форма контеста).
	partials, _ := filepath.Glob(filepath.Join(r.templatesDir, "*.partial.html"))
	files = append(files, partials...)
	files = append(files, filepath.Join(r.templatesDir, pageTemplate))

	var fp strings.Builder
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			fp.WriteString(f + "|missing;")
			continue
		}
		fmt.Fprintf(&fp, "%s|%d|%d;", f, info.Size(), info.ModTime().UnixNano())
	}
	fingerprint := fp.String()

	r.cacheMu.RLock()
	if c, ok := r.cache[pageTemplate]; ok && c.fingerprint == fingerprint {
		r.cacheMu.RUnlock()
		return c.tmpl, nil
	}
	r.cacheMu.RUnlock()

	tmpl, err := template.New("layout.html").Funcs(r.funcMap).ParseFiles(files...)
	if err != nil {
		return nil, err
	}

	r.cacheMu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]cachedTemplate)
	}
	r.cache[pageTemplate] = cachedTemplate{fingerprint: fingerprint, tmpl: tmpl}
	r.cacheMu.Unlock()
	return tmpl, nil
}

// TaskCell — подготовленная к рендеру ячейка задачи в строке standings.
type TaskCell struct {
	IsIOI    bool
	Status   string // класс статуса (solved/attempted/none) для не-IOI
	Text     string // символ или баллы; для дорешки уже обёрнуто в скобки
	Alpha    string // прозрачность фона для IOI
	Practice bool   // дорешка: добавляет CSS-класс practice
	Accepted bool   // «зачтено» (не полный OK): добавляет CSS-класс accepted
	// SubmissionURL — ссылка на список посылок ученика по этой задаче (если у
	// него есть посылка и сайт это поддерживает). Пусто — ячейка не кликабельна.
	SubmissionURL string
}

// taskCells объединяет статусы, баллы и пометку дорешки в единый набор ячеек.
// Для IOI основной балл и дорешка показываются вместе: «50 (70)» — 50 в окне,
// 70 в дорешке; только дорешка — «(70)». Фон ячейки — по лучшему из них.
func taskCells(contest domain.GeneratedContestStandings, row domain.GeneratedRow) []TaskCell {
	isIOI := contest.ScoreSystem.IsIOI()
	cells := make([]TaskCell, 0, len(row.Statuses))
	for i := range row.Statuses {
		practice := i < len(row.Upsolved) && row.Upsolved[i]
		accepted := i < len(row.Accepted) && row.Accepted[i]
		cell := TaskCell{IsIOI: isIOI, Practice: practice, Accepted: accepted}

		// Есть хотя бы одна посылка (решено/попытка) → делаем ячейку ссылкой на
		// посылки ученика по этой задаче.
		if s := row.Statuses[i]; s == domain.TaskStatusSolved || s == domain.TaskStatusAttempted {
			if i < len(contest.Tasks) {
				cell.SubmissionURL = submissionURL(contest.Tasks[i].URL, row.Accounts)
			}
		}

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

// UpsolvingView — таблица «без дорешки» (второй tbody). Строки уже отсортированы
// и с проставленными местами по результату только в окне контеста. Клиентский
// переключатель просто показывает этот tbody вместо обычного — у каждого
// зрителя свой выбор, данные для всех одни.
type UpsolvingView struct {
	Rows       []UpsolvingRow
	HasPenalty bool // показывать ли колонку «Штраф» в оконном виде
}

// UpsolvingRow — одна строка оконного вида, готовая к рендеру.
type UpsolvingRow struct {
	StudentID      string
	PublicName     string
	Place          string
	Count          int    // «Баллы» (ioi) или «Решено» (edu)
	Penalty        string // текст колонки «Штраф» (если есть)
	ProviderStatus string
	Cells          []TaskCell
}

// upsolvingView возвращает nil, если в контесте нет дорешки (кнопка не нужна).
// Иначе строит вид «только окно»: у tasks-контестов места/порядок
// пересчитываются той же логикой, что при сборке; у provider-контестов (CF)
// место и порядок — по результатам во время контеста (дорешка на них и так не
// влияла), меняются только суммы и ячейки.
func upsolvingView(contest domain.GeneratedContestStandings) *UpsolvingView {
	hasUpsolving := false
	for _, row := range contest.Rows {
		for _, u := range row.Upsolved {
			if u {
				hasUpsolving = true
			}
		}
		for _, p := range row.PracticeScores {
			if p != nil {
				hasUpsolving = true
			}
		}
	}
	if !hasUpsolving {
		return nil
	}

	isIOI := contest.ScoreSystem.IsIOI()
	isProvider := contest.ContestType == domain.ContestTypeProvider
	hasPenalty := hasPenaltyColumn(contest.Rows)

	type built struct {
		out   UpsolvingRow
		score int
		count int
	}
	items := make([]built, len(contest.Rows))
	for ri, row := range contest.Rows {
		ups := func(i int) bool { return i < len(row.Upsolved) && row.Upsolved[i] }

		cells := taskCells(contest, row)
		winCells := make([]TaskCell, len(cells))
		windowScore, windowSolved, zeros := 0, 0, 0
		for i := range cells {
			wc := cells[i]
			wc.Practice = false
			solvedInWindow := row.Statuses[i] == domain.TaskStatusSolved && !ups(i)
			if isIOI {
				// Балл в окне: у tasks Scores[i] всегда оконный; у provider для
				// дорешанной задачи Scores[i] — это балл дорешки, поэтому её обнуляем.
				var main *int
				if i < len(row.Scores) && !(isProvider && ups(i)) {
					main = row.Scores[i]
				}
				wc.Text = scoreText(main)
				wc.Alpha = scoreAlpha(main)
				if main != nil {
					windowScore += *main
				}
				if main == nil || *main <= 0 {
					zeros++
				}
			} else {
				if solvedInWindow {
					wc.Status, wc.Text = "solved", statusSymbol(domain.TaskStatusSolved)
				} else if row.Statuses[i] == domain.TaskStatusAttempted && !ups(i) {
					wc.Status, wc.Text = "attempted", statusSymbol(domain.TaskStatusAttempted)
				} else {
					wc.Status, wc.Text = "none", ""
				}
			}
			if solvedInWindow {
				windowSolved++
			} else {
				wc.Accepted = false // рамка «зачтено» только у оконного решения
			}
			if wc.Text == "" {
				wc.SubmissionURL = ""
			}
			winCells[i] = wc
		}

		penaltyStr := ""
		if hasPenalty {
			penaltyStr = penaltyText(row.Penalty)
		}
		if !isProvider && isIOI && contest.ZeroPenalty > 0 {
			penalty := zeros * contest.ZeroPenalty
			windowScore -= penalty
			penaltyStr = fmt.Sprintf("%d", penalty)
		}

		count := windowSolved
		if isIOI {
			count = windowScore
		}
		items[ri] = built{
			out: UpsolvingRow{
				StudentID: row.StudentID, PublicName: row.PublicName, Place: row.Place, Count: count,
				Penalty: penaltyStr, ProviderStatus: row.ProviderStatus, Cells: winCells,
			},
			score: windowScore, count: windowSolved,
		}
	}

	// Порядок и места. Провайдеры уже упорядочены по местам во время контеста —
	// сохраняем их. Для tasks пересортировываем и переставляем места по окну.
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	if !isProvider {
		sort.SliceStable(order, func(a, b int) bool {
			ia, ib := items[order[a]], items[order[b]]
			if isIOI && ia.score != ib.score {
				return ia.score > ib.score
			}
			if ia.count != ib.count {
				return ia.count > ib.count
			}
			return strings.ToLower(ia.out.PublicName) < strings.ToLower(ib.out.PublicName)
		})
	}

	view := &UpsolvingView{HasPenalty: hasPenalty, Rows: make([]UpsolvingRow, 0, len(items))}
	ranked := make([]domain.GeneratedRow, len(order))
	for pos, ri := range order {
		ranked[pos] = domain.GeneratedRow{TotalScore: items[ri].score, SolvedCount: items[ri].count}
	}
	if !isProvider {
		domain.AssignContestPlaces(ranked, isIOI)
	}
	for pos, ri := range order {
		r := items[ri].out
		if !isProvider {
			r.Place = ranked[pos].Place
		}
		view.Rows = append(view.Rows, r)
	}
	return view
}

// submissionURL строит ссылку на список посылок ученика по задаче. Пока умеет
// только informatics: к её URL задачи (…/view.php?chapterid=N#i) добавляются
// параметры &submit&user_id=<acc>, фрагмент (#i — номер задачи в главе)
// сохраняется. Пусто — сайт не поддерживается или нет аккаунта.
func submissionURL(taskURL string, accounts map[string]string) string {
	taskURL = strings.TrimSpace(taskURL)
	if taskURL == "" || len(accounts) == 0 {
		return ""
	}
	u, err := url.Parse(taskURL)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Hostname()) {
	case "informatics.msk.ru", "www.informatics.msk.ru",
		"informatics.mccme.ru", "www.informatics.mccme.ru":
		acc := strings.TrimSpace(accounts["informatics"])
		if acc == "" {
			return ""
		}
		// Дописываем к сырой строке запроса, чтобы «submit» остался без значения.
		if u.RawQuery != "" {
			u.RawQuery += "&"
		}
		u.RawQuery += "submit&user_id=" + url.QueryEscape(acc)
		return u.String()
	}
	return ""
}

// submissionLink — ссылка на посылки ученика по задаче для профиля: собирает
// карту сайт→account_id из аккаунтов ученика и зовёт submissionURL. Пусто —
// сайт не поддерживается или нет аккаунта.
func submissionLink(taskURL string, accounts []domain.Account) string {
	if len(accounts) == 0 {
		return ""
	}
	m := make(map[string]string, len(accounts))
	for _, a := range accounts {
		site := domain.NormalizeSite(a.Site)
		id := strings.TrimSpace(a.AccountID)
		if site == "" || id == "" {
			continue
		}
		if _, ok := m[site]; !ok {
			m[site] = id
		}
	}
	return submissionURL(taskURL, m)
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

// contestGeneratedAtISO — то же время в ISO 8601 (для «живого» относительного
// времени и обратного отсчёта в JS). Пусто — времени нет.
func contestGeneratedAtISO(generatedAt *time.Time) string {
	if generatedAt == nil || generatedAt.IsZero() {
		return ""
	}
	return generatedAt.Format(time.RFC3339)
}

// timeISO — момент в ISO 8601 (RFC3339) для «живого» отсчёта в JS. Пусто — нет.
func timeISO(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
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
