package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"standings-edu/internal/domain"
)

const ManualTableProviderID = "manual_table"

// ManualTablesFileName — файл с таблицами оценок кондуитов, лежит рядом с
// contests.json (глобальным или группы): map contest_id -> TSV-таблица.
// Определение контеста в contests.json остаётся без оценок; загрузчик
// подставляет таблицу в provider_config перед генерацией. Таблица прямо в
// provider_config (старый формат) тоже принимается — файл имеет приоритет.
const ManualTablesFileName = "manual_tables.json"

// InjectManualTable подставляет таблицу кондуита в provider_config (заменяя
// поле table). Пустой конфиг превращается в {"table": ...}.
func InjectManualTable(cfg json.RawMessage, table string) (json.RawMessage, error) {
	m := map[string]any{}
	if len(bytes.TrimSpace(cfg)) > 0 {
		if err := json.Unmarshal(cfg, &m); err != nil {
			return nil, fmt.Errorf("provider_config: %w", err)
		}
	}
	m["table"] = table
	return json.Marshal(m)
}

// StripManualTable убирает таблицу из provider_config (обратная операция:
// оценки уезжают в manual_tables.json). Возвращает конфиг без поля table,
// саму таблицу и был ли ключ table в конфиге вообще (правка сырым JSON без
// table не должна затирать сохранённые оценки).
func StripManualTable(cfg json.RawMessage) (json.RawMessage, string, bool, error) {
	m := map[string]any{}
	if len(bytes.TrimSpace(cfg)) > 0 {
		if err := json.Unmarshal(cfg, &m); err != nil {
			return nil, "", false, fmt.Errorf("provider_config: %w", err)
		}
	}
	raw, had := m["table"]
	table, _ := raw.(string)
	delete(m, "table")
	out, err := json.Marshal(m)
	if err != nil {
		return nil, "", false, err
	}
	return out, table, had, nil
}

// ManualTableProvider строит таблицу контеста из вручную вставленных оценок
// (кондуит математиков): данные лежат прямо в provider_config как текст,
// скопированный из Google Таблиц/Excel (колонки разделены табуляцией). Первая
// колонка — ФИО, дальше оценки: пусто — задача не сдана, «1» или «+» — сдана,
// любые другие числа тоже поддерживаются. Сеть не нужна.
type ManualTableProvider struct{}

func NewManualTableProvider() *ManualTableProvider { return &ManualTableProvider{} }

func (p *ManualTableProvider) ProviderID() string { return ManualTableProviderID }

// manualTableConfig — provider_config контеста manual_table.
type manualTableConfig struct {
	// Table — вставленная таблица: строки — ученики, колонки — через табуляцию
	// (как копирует Google Таблицы/Excel). Первая строка может быть заголовком
	// с названиями колонок (распознаётся по слову «ФИО»/«Ученик» или буквам в
	// ячейках оценок).
	Table string `json:"table"`
	// ShowAll — показывать строки, не сопоставленные с учениками группы,
	// отдельными строками с именем из таблицы.
	ShowAll bool `json:"show_all,omitempty"`
	// TaskCount — фиксированное число колонок-задач. Задано (>0) — таблица
	// обрезается/дополняется до этого числа, а сама может быть и пустой
	// (кондуит «на будущее»: колонки есть, оценок ещё нет). 0 — число колонок
	// выводится из самой широкой строки таблицы, как раньше.
	TaskCount int `json:"task_count,omitempty"`
}

func parseManualTableConfig(raw json.RawMessage) (manualTableConfig, error) {
	var cfg manualTableConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return cfg, fmt.Errorf("provider_config is required for provider=%q", ManualTableProviderID)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode provider_config: %w", err)
	}
	if cfg.TaskCount < 0 || cfg.TaskCount > 200 {
		return cfg, fmt.Errorf("provider_config.task_count must be in 0..200, got %d", cfg.TaskCount)
	}
	// Пустая таблица допустима только при заданном числе задач: контест уже
	// создан (колонки известны), оценки заполнят позже.
	if strings.TrimSpace(cfg.Table) == "" && cfg.TaskCount <= 0 {
		return cfg, fmt.Errorf("provider_config.table is required (вставьте таблицу с оценками или задайте task_count)")
	}
	return cfg, nil
}

// manualCell — распознанная ячейка оценки.
type manualCell struct {
	status string
	score  *int
}

// manualRow — строка таблицы: имя ученика и его оценки.
type manualRow struct {
	name  string
	cells []manualCell
}

// parseManualTable разбирает вставленный текст: имена колонок (из заголовка или
// номера 1..N) и строки учеников с оценками. fixedTaskCount > 0 фиксирует число
// колонок (строки обрезаются/дополняются), и тогда таблица может быть пустой;
// 0 — число колонок берётся по самой широкой строке.
func parseManualTable(raw string, fixedTaskCount int) ([]string, []manualRow, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	lines := make([][]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cells := strings.Split(line, "\t")
		for i := range cells {
			cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], "\u00a0", " "))
		}
		lines = append(lines, cells)
	}
	if len(lines) == 0 {
		if fixedTaskCount > 0 {
			return manualHeaderLabels(nil, fixedTaskCount), nil, nil
		}
		return nil, nil, fmt.Errorf("таблица пуста")
	}

	taskCount := fixedTaskCount
	if taskCount <= 0 {
		maxCols := 0
		for _, cells := range lines {
			if len(cells) > maxCols {
				maxCols = len(cells)
			}
		}
		if maxCols < 2 {
			return nil, nil, fmt.Errorf("не найдено колонок с оценками: колонки должны разделяться табуляцией (копируйте таблицу из Google Таблиц/Excel)")
		}
		taskCount = maxCols - 1
	}

	var header []string
	if isManualHeaderRow(lines[0]) {
		header = lines[0]
		lines = lines[1:]
		if len(lines) == 0 {
			if fixedTaskCount > 0 {
				return manualHeaderLabels(header, taskCount), nil, nil
			}
			return nil, nil, fmt.Errorf("в таблице только заголовок — нет строк с учениками")
		}
	}

	labels := manualHeaderLabels(header, taskCount)

	rows := make([]manualRow, 0, len(lines))
	for _, cells := range lines {
		name := cells[0]
		if name == "" {
			continue
		}
		row := manualRow{name: name, cells: make([]manualCell, taskCount)}
		for i := 0; i < taskCount; i++ {
			cell := ""
			if i+1 < len(cells) {
				cell = cells[i+1]
			}
			row.cells[i] = parseManualCell(cell)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("не найдено строк с учениками (первая колонка — ФИО)")
	}
	return labels, rows, nil
}

// SplitManualTable разбирает TSV кондуита на СЫРЫЕ строки для редактора:
// имена колонок (по заголовку либо номера 1..N с учётом fixedTaskCount) и
// строки [имя, значения…] как введены. Ошибок нет — редактору годится любой
// текст (битые строки просто отбрасываются).
func SplitManualTable(raw string, fixedTaskCount int) (labels []string, rows [][]string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := make([][]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cells := strings.Split(line, "\t")
		for i := range cells {
			cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], "\u00a0", " "))
		}
		lines = append(lines, cells)
	}

	taskCount := fixedTaskCount
	if taskCount <= 0 {
		maxCols := 1
		for _, cells := range lines {
			if len(cells) > maxCols {
				maxCols = len(cells)
			}
		}
		taskCount = maxCols - 1
	}
	if taskCount < 1 {
		taskCount = 1
	}

	var header []string
	if len(lines) > 0 && isManualHeaderRow(lines[0]) {
		header = lines[0]
		lines = lines[1:]
	}
	labels = manualHeaderLabels(header, taskCount)

	rows = make([][]string, 0, len(lines))
	for _, cells := range lines {
		if cells[0] == "" {
			continue
		}
		row := make([]string, taskCount+1)
		row[0] = cells[0]
		for i := 0; i < taskCount && i+1 < len(cells); i++ {
			row[i+1] = cells[i+1]
		}
		rows = append(rows, row)
	}
	return labels, rows
}

// manualHeaderLabels — имена колонок: из строки-заголовка (первая ячейка —
// «ФИО»), пустые/недостающие — порядковые номера 1..N.
func manualHeaderLabels(header []string, taskCount int) []string {
	labels := make([]string, taskCount)
	for i := range labels {
		if header != nil && i+1 < len(header) && header[i+1] != "" {
			labels[i] = header[i+1]
		} else {
			labels[i] = strconv.Itoa(i + 1)
		}
	}
	return labels
}

// isManualHeaderRow распознаёт строку-заголовок: первая ячейка — «ФИО»/«Ученик»/
// пусто, либо в ячейках оценок есть буквы (названия колонок). Чисто числовые
// заголовки не распознаются — назовите колонки словами или уберите заголовок.
func isManualHeaderRow(cells []string) bool {
	first := strings.ToLower(strings.TrimSpace(cells[0]))
	switch {
	case first == "", first == "фио", first == "ученик", first == "имя", first == "фамилия", first == "участник", first == "name":
		return true
	}
	for _, c := range cells[1:] {
		for _, r := range c {
			if r != '+' && r != '±' && (r == 'ё' || r == 'Ё' || (r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
				return true
			}
		}
	}
	return false
}

// parseManualCell — семантика кондуита: пусто/точка/тире — не сдана; «+» или
// ненулевое число — сдана; «±» или 0 — попытка. Десятичная запятая («0,5»)
// поддерживается, дробные округляются (баллы в таблицах целые).
func parseManualCell(s string) manualCell {
	s = strings.TrimSpace(s)
	switch s {
	case "", ".", "-", "—", "–":
		return manualCell{status: domain.TaskStatusNone}
	case "+":
		one := 1
		return manualCell{status: domain.TaskStatusSolved, score: &one}
	case "±", "+-", "+/-":
		return manualCell{status: domain.TaskStatusAttempted}
	}
	if v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64); err == nil {
		score := int(math.Round(v))
		if v != 0 {
			return manualCell{status: domain.TaskStatusSolved, score: &score}
		}
		return manualCell{status: domain.TaskStatusAttempted, score: &score}
	}
	// Нераспознанный текст («н», «болел») — как отсутствие оценки.
	return manualCell{status: domain.TaskStatusNone}
}

func (p *ManualTableProvider) BuildStandings(_ context.Context, input ContestProviderInput) (domain.GeneratedContestStandings, error) {
	cfg, err := parseManualTableConfig(input.Contest.ProviderConfig)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}
	labels, rows, err := parseManualTable(cfg.Table, cfg.TaskCount)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	isIOI := input.Contest.ScoreSystem.IsIOI()
	tasks := make([]domain.GeneratedTask, 0, len(labels))
	for _, label := range labels {
		tasks = append(tasks, domain.GeneratedTask{Label: label})
	}
	out := domain.GeneratedContestStandings{
		ID:          input.Contest.ID,
		Title:       input.Contest.Title,
		ScoreSystem: input.Contest.ScoreSystem.Normalized(),
		Subcontests: []domain.GeneratedSubcontest{{
			Title:     "Задачи",
			TaskCount: len(tasks),
			Tasks:     tasks,
		}},
		Tasks: tasks,
		Rows:  make([]domain.GeneratedRow, 0, len(input.Students)),
	}

	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.name
	}
	matched := matchNamesToStudents(names, input.Students)
	rowByStudent := make(map[string]manualRow, len(matched))
	for idx, sid := range matched {
		rowByStudent[sid] = rows[idx]
	}

	appendRow := func(studentID, publicName string, src *manualRow) {
		row := domain.GeneratedRow{
			StudentID:  studentID,
			PublicName: publicName,
			Statuses:   make([]string, len(tasks)),
		}
		for i := range row.Statuses {
			row.Statuses[i] = domain.TaskStatusNone
		}
		if isIOI {
			row.Scores = make([]*int, len(tasks))
		}
		if src != nil {
			for i, cell := range src.cells {
				row.Statuses[i] = cell.status
				if cell.status == domain.TaskStatusSolved {
					row.SolvedCount++
				}
				if isIOI && cell.score != nil {
					value := *cell.score
					row.Scores[i] = &value
					row.TotalScore += value
				}
			}
		}
		out.Rows = append(out.Rows, row)
	}

	for _, student := range input.Students {
		if src, ok := rowByStudent[student.ID]; ok {
			appendRow(student.ID, student.PublicName, &src)
		} else {
			appendRow(student.ID, student.PublicName, nil)
		}
	}

	if cfg.ShowAll {
		matchedIdx := make(map[int]struct{}, len(matched))
		for idx := range matched {
			matchedIdx[idx] = struct{}{}
		}
		extra := 0
		for idx := range rows {
			if _, ok := matchedIdx[idx]; ok {
				continue
			}
			extra++
			appendRow(fmt.Sprintf("manual_extra_row_%d", extra), rows[idx].name, &rows[idx])
		}
	}

	sortMoodleRows(out.Rows, isIOI)
	return out, nil
}
