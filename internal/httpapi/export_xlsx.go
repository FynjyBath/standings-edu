package httpapi

// Экспорт группы в .xlsx: листы — вкладки сводной (по table_name контестов,
// плюс «Все»), лист «Оценки» — с живыми формулами (веса — редактируемые
// ячейки, итог пересчитывается). Совместимо с импортом в Google Таблицы.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/xlsx"
)

// GroupExportXLSX отдаёт экспорт группы по токену преподавателя.
func (h *Handlers) GroupExportXLSX(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if !domain.IsValidSlug(slug) || !h.groupTokenValid(slug, token) {
		http.NotFound(w, r)
		return
	}
	h.serveGroupExportXLSX(w, slug)
}

// AdminGroupExportXLSX — тот же экспорт из админки (?slug=...).
func (h *Handlers) AdminGroupExportXLSX(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if !domain.IsValidSlug(slug) {
		http.NotFound(w, r)
		return
	}
	h.serveGroupExportXLSX(w, slug)
}

func (h *Handlers) serveGroupExportXLSX(w http.ResponseWriter, slug string) {
	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	// Вид преподавателя: полные версии замороженных таблиц, скрытое видно.
	standings.SwapInFullRows()

	var gradesCfg *domain.GradesConfig
	if gf, ok := h.readSourceGroupFile(slug); ok {
		gradesCfg = gf.Grades
	}
	manual := h.readManualGrades(slug)

	wb := buildGroupExportWorkbook(standings, gradesCfg, manual)
	var buf bytes.Buffer
	if err := wb.Write(&buf); err != nil {
		h.logger.Printf("ERROR export xlsx slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := slug + "-" + time.Now().Format("2006-01-02") + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write(buf.Bytes())
}

// readManualGrades читает ручные оценки группы (пустая карта при отсутствии).
func (h *Handlers) readManualGrades(slug string) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	body, err := os.ReadFile(filepath.Join(h.dataDir, "groups", slug, "grades_manual.json"))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(body, &out)
	return out
}

// ── Построение книги ─────────────────────────────────────────────────────────

const exportAllTab = "Все"

// contestPlacement — где контест лежит на листе вкладки (для формул оценок).
type contestPlacement struct {
	firstCol, lastCol int  // 0-based колонки на листе
	totalOnly         bool // одна колонка-сумма вместо колонок задач
	isIoi             bool
	taskCount         int
}

type exportStudent struct {
	id   string
	name string
}

func buildGroupExportWorkbook(std domain.GeneratedGroupStandings, gradesCfg *domain.GradesConfig, manual map[string]map[string]float64) *xlsx.Workbook {
	students := exportStudents(std)
	wb := &xlsx.Workbook{}

	// Вкладки в порядке первого появления table_name (как в сводной) + «Все».
	tabs := []string{exportAllTab}
	seen := map[string]bool{}
	for _, c := range std.Contests {
		for _, name := range c.TableNames {
			name = strings.TrimSpace(name)
			if name != "" && !seen[name] {
				seen[name] = true
				tabs = append(tabs, name)
			}
		}
	}

	// placements[вкладка][contestID] — позиции контестов на листе вкладки.
	placements := make(map[string]map[string]contestPlacement, len(tabs))
	sheetNames := make(map[string]string, len(tabs))
	for _, tab := range tabs {
		contests := contestsOfTab(std, tab)
		if len(contests) == 0 {
			continue
		}
		sheet := wb.AddSheet(tab)
		sheetNames[tab] = sheet.Name
		placements[tab] = fillContestsSheet(sheet, contests, students)
	}

	if gradesCfg != nil && len(gradesCfg.Columns) > 0 {
		fillGradesSheet(wb.AddSheet("Оценки"), gradesCfg, manual, students, std, sheetNames, placements)
	}
	return wb
}

// exportStudents — участники группы из строк всех контестов, по алфавиту.
// Порядок ОДИНАКОВ на всех листах: формулы листа «Оценки» ссылаются на строки
// листов вкладок по номеру.
func exportStudents(std domain.GeneratedGroupStandings) []exportStudent {
	byID := map[string]string{}
	for _, c := range std.Contests {
		for _, row := range c.Rows {
			if row.StudentID != "" && byID[row.StudentID] == "" {
				name := row.PublicName
				if name == "" {
					name = row.StudentID
				}
				byID[row.StudentID] = name
			}
		}
	}
	out := make([]exportStudent, 0, len(byID))
	for id, name := range byID {
		out = append(out, exportStudent{id: id, name: name})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strings.ToLower(out[i].name), strings.ToLower(out[j].name); a != b {
			return a < b
		}
		return out[i].id < out[j].id
	})
	return out
}

func contestsOfTab(std domain.GeneratedGroupStandings, tab string) []domain.GeneratedContestStandings {
	out := make([]domain.GeneratedContestStandings, 0)
	for _, c := range std.Contests {
		if len(c.Tasks) == 0 {
			continue
		}
		if tab == exportAllTab {
			out = append(out, c)
			continue
		}
		for _, name := range c.TableNames {
			if strings.TrimSpace(name) == tab {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// fillContestsSheet — лист вкладки: A «Ученик», B «Σ баллов», C «Σ +», дальше
// контесты (колонки задач или одна колонка-сумма). Возвращает позиции
// контестов для формул оценок.
func fillContestsSheet(sheet *xlsx.Sheet, contests []domain.GeneratedContestStandings, students []exportStudent) map[string]contestPlacement {
	placement := map[string]contestPlacement{}

	head1 := []xlsx.Cell{xlsx.Header("Ученик"), xlsx.Header("Σ баллов"), xlsx.Header("Σ +")}
	head2 := []xlsx.Cell{{}, {}, {}}
	sheet.Merges = append(sheet.Merges, "A1:A2", "B1:B2", "C1:C2")
	sheet.ColWidths[0] = 24
	sheet.ColWidths[1] = 9
	sheet.ColWidths[2] = 7

	col := 3
	rowMaps := make([]map[string]domain.GeneratedRow, len(contests))
	for ci, c := range contests {
		rowMap := make(map[string]domain.GeneratedRow, len(c.Rows))
		for _, row := range c.Rows {
			rowMap[row.StudentID] = row
		}
		rowMaps[ci] = rowMap

		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = c.ID
		}
		if c.FrozenAt != nil {
			title += " ❄"
		}
		isIoi := c.ScoreSystem.Normalized() == domain.ScoreSystemIOI

		if c.SummaryTotalOnly {
			label := strings.TrimSpace(c.ShortName)
			if label == "" {
				label = title
			}
			head1 = append(head1, xlsx.Header(label))
			head2 = append(head2, xlsx.Cell{})
			sheet.Merges = append(sheet.Merges, xlsx.CellRef(col, 0)+":"+xlsx.CellRef(col, 1))
			sheet.ColWidths[col] = 9
			placement[c.ID] = contestPlacement{firstCol: col, lastCol: col, totalOnly: true, isIoi: isIoi, taskCount: len(c.Tasks)}
			col++
			continue
		}

		head1 = append(head1, xlsx.Header(title))
		for i := len(c.Tasks); i > 1; i-- {
			head1 = append(head1, xlsx.Cell{})
		}
		if len(c.Tasks) > 1 {
			sheet.Merges = append(sheet.Merges, xlsx.CellRef(col, 0)+":"+xlsx.CellRef(col+len(c.Tasks)-1, 0))
		}
		for i, t := range c.Tasks {
			head2 = append(head2, xlsx.Header(t.Label))
			sheet.ColWidths[col+i] = 5
		}
		placement[c.ID] = contestPlacement{firstCol: col, lastCol: col + len(c.Tasks) - 1, isIoi: isIoi, taskCount: len(c.Tasks)}
		col += len(c.Tasks)
	}
	sheet.Rows = append(sheet.Rows, head1, head2)
	sheet.FreezeRows = 2
	sheet.FreezeCols = 1

	lastCol := col - 1
	for si, st := range students {
		r := 2 + si // 0-based строка ученика
		row := make([]xlsx.Cell, 3, col)
		row[0] = xlsx.Text(st.name)
		if lastCol >= 3 {
			rangeRef := xlsx.CellRef(3, r) + ":" + xlsx.CellRef(lastCol, r)
			// SUM игнорирует текстовые «+», считает только числа (баллы ioi и
			// колонки-суммы); COUNTIF считает плюсы edu-задач.
			row[1] = xlsx.Formula("SUM(" + rangeRef + ")")
			plus := "COUNTIF(" + rangeRef + ",\"+\")"
			for _, c := range contests {
				if c.SummaryTotalOnly && c.ScoreSystem.Normalized() != domain.ScoreSystemIOI {
					plus += "+N(" + xlsx.CellRef(placement[c.ID].firstCol, r) + ")"
				}
			}
			row[2] = xlsx.Formula(plus)
		}
		for ci, c := range contests {
			srow, has := rowMaps[ci][st.id]
			p := placement[c.ID]
			if c.SummaryTotalOnly {
				row = append(row, totalOnlyCell(c, srow, has, p.isIoi))
				continue
			}
			for i := range c.Tasks {
				row = append(row, taskCell(c, srow, has, i, p.isIoi))
			}
		}
		sheet.Rows = append(sheet.Rows, row)
	}

	// Подсветка как на сайте: «+» — зелёная, «−» — красная (edu-задачи);
	// баллы ioi — белый→зелёный (0..100); колонки-суммы — красный→жёлтый→
	// зелёный по доле от максимума.
	if len(students) > 0 {
		lastRow := 2 + len(students) - 1
		for _, c := range contests {
			p := placement[c.ID]
			sqref := xlsx.CellRef(p.firstCol, 2) + ":" + xlsx.CellRef(p.lastCol, lastRow)
			switch {
			case p.totalOnly:
				max := float64(p.taskCount)
				if p.isIoi {
					max *= 100
				}
				sheet.CondFmts = append(sheet.CondFmts, xlsx.CondFmt{
					Sqref: sqref, Scale: true, Min: 0, Max: max,
					Colors: []string{"E19E9E", "E1E19E", "9EE19E"},
				})
			case p.isIoi:
				sheet.CondFmts = append(sheet.CondFmts, xlsx.CondFmt{
					Sqref: sqref, Scale: true, Min: 0, Max: 100,
					Colors: []string{"FFFFFF", "50B46E"},
				})
			default:
				sheet.CondFmts = append(sheet.CondFmts,
					xlsx.CondFmt{Sqref: sqref, Text: "+", Good: true},
					xlsx.CondFmt{Sqref: sqref, Text: "−"},
				)
			}
		}
	}
	return placement
}

// totalOnlyCell — ячейка контеста «только сумма»: число с форматом «N / макс»
// (как «2 / 24» на сайте) — значение остаётся числом для формул.
func totalOnlyCell(c domain.GeneratedContestStandings, row domain.GeneratedRow, has bool, isIoi bool) xlsx.Cell {
	if !has {
		return xlsx.Cell{}
	}
	max := len(c.Tasks)
	if isIoi {
		max *= 100
	}
	numFmt := `0" / ` + strconv.Itoa(max) + `"`
	value := row.SolvedCount
	if isIoi {
		value = row.TotalScore
	}
	return xlsx.Cell{Kind: xlsx.CellNumber, Value: strconv.Itoa(value), NumFmt: numFmt}
}

// taskCell — ячейка задачи: edu — «+» (решена, вкл. дорешку) / «−» (попытки) /
// пусто; ioi — эффективный балл числом (максимум из основного и дорешки).
func taskCell(c domain.GeneratedContestStandings, row domain.GeneratedRow, has bool, i int, isIoi bool) xlsx.Cell {
	if !has {
		return xlsx.Cell{}
	}
	status := ""
	if i < len(row.Statuses) {
		status = row.Statuses[i]
	}
	if !isIoi {
		switch status {
		case domain.TaskStatusSolved:
			return xlsx.Text("+")
		case domain.TaskStatusAttempted:
			return xlsx.Text("−")
		}
		return xlsx.Cell{}
	}
	best, ok := 0, false
	if i < len(row.Scores) && row.Scores[i] != nil {
		best, ok = *row.Scores[i], true
	}
	if i < len(row.PracticeScores) && row.PracticeScores[i] != nil && *row.PracticeScores[i] > best {
		best, ok = *row.PracticeScores[i], true
	}
	if !ok {
		if status == domain.TaskStatusAttempted {
			return xlsx.Text("−")
		}
		return xlsx.Cell{}
	}
	return xlsx.Number(strconv.Itoa(best))
}

// ── Лист «Оценки» ────────────────────────────────────────────────────────────

// sheetRef — ссылка на лист в формуле: 'Имя'!— с экранированием апострофов.
func sheetRef(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'!"
}

// fillGradesSheet — таблица оценок с формулами: табличные столбцы считаются с
// листов вкладок (COUNTIF/SUM), нормировка и веса — открытые части формул;
// веса лежат в ячейках второй строки и редактируемы.
func fillGradesSheet(sheet *xlsx.Sheet, cfg *domain.GradesConfig, manual map[string]map[string]float64, students []exportStudent, std domain.GeneratedGroupStandings, sheetNames map[string]string, placements map[string]map[string]contestPlacement) {
	decimals := 1
	if cfg.Round != nil && *cfg.Round >= 0 {
		decimals = *cfg.Round
	}

	type colInfo struct {
		cfg      domain.GradeColumn
		rawCol   int // -1 — нет колонки «сырое» (ручной столбец)
		gradeCol int
	}
	cols := make([]colInfo, 0, len(cfg.Columns))

	head := []xlsx.Cell{xlsx.Header("Ученик")}
	weightsRow := []xlsx.Cell{xlsx.Muted("вес →")}
	sheet.ColWidths[0] = 24
	col := 1
	for _, c := range cfg.Columns {
		info := colInfo{cfg: c, rawCol: -1}
		if strings.EqualFold(c.Type, domain.GradeColumnTable) {
			info.rawCol = col
			head = append(head, xlsx.Muted(c.Title+" — сырое"))
			weightsRow = append(weightsRow, xlsx.Cell{})
			sheet.ColWidths[col] = 12
			col++
		}
		info.gradeCol = col
		head = append(head, xlsx.Header(c.Title+" (0–10)"))
		weight := c.Weight
		if weight <= 0 {
			weight = 1
		}
		weightsRow = append(weightsRow, xlsx.MutedNumber(trimFloat(weight)))
		sheet.ColWidths[col] = 12
		col++
		cols = append(cols, info)
	}
	finalCol := col
	head = append(head, xlsx.Header("Итог"))
	weightsRow = append(weightsRow, xlsx.Cell{})
	sheet.ColWidths[finalCol] = 8
	sheet.Rows = append(sheet.Rows, head, weightsRow)
	sheet.FreezeRows = 2
	sheet.FreezeCols = 1

	firstStudentRow := 2
	lastStudentRow := firstStudentRow + len(students) - 1

	for si, st := range students {
		r := firstStudentRow + si
		row := make([]xlsx.Cell, finalCol+1)
		row[0] = xlsx.Text(st.name)

		for _, info := range cols {
			if info.rawCol < 0 {
				// Ручной столбец: значение как есть (пусто — оценки нет).
				if v, ok := manual[info.cfg.ID][st.id]; ok {
					row[info.gradeCol] = xlsx.Number(trimFloat(v))
				}
				continue
			}
			raw, ok := tableRawFormula(info.cfg, std, sheetNames, placements, r)
			if !ok {
				continue
			}
			row[info.rawCol] = xlsx.Cell{Kind: xlsx.CellFormula, Value: raw, Style: xlsx.StyleMuted}
			rawRef := xlsx.CellRef(info.rawCol, r)
			ref := normalizeRefFormula(info.cfg, std, info.rawCol, firstStudentRow, lastStudentRow)
			row[info.gradeCol] = xlsx.Formula("IFERROR(MIN(10,MAX(0," + rawRef + "/" + ref + "*10)),\"\")")
		}

		// Итог: взвешенное среднее по непустым столбцам; веса — из строки 2.
		var num, den []string
		for _, info := range cols {
			v := xlsx.CellRef(info.gradeCol, r)
			w := xlsx.ColName(info.gradeCol) + "$2"
			num = append(num, w+"*IF("+v+"=\"\",0,"+v+")")
			den = append(den, w+"*IF("+v+"=\"\",0,1)")
		}
		row[finalCol] = xlsx.Formula("IFERROR(ROUND((" + strings.Join(num, "+") + ")/(" + strings.Join(den, "+") + ")," + strconv.Itoa(decimals) + "),\"\")")
		sheet.Rows = append(sheet.Rows, row)
	}
}

// tableRawFormula — формула сырого значения табличного столбца оценки для
// строки ученика r (0-based): plus — COUNTIF плюсов + колонки-суммы edu;
// score — SUM баллов ioi + колонки-суммы ioi. Ссылается на лист вкладки.
func tableRawFormula(c domain.GradeColumn, std domain.GeneratedGroupStandings, sheetNames map[string]string, placements map[string]map[string]contestPlacement, r int) (string, bool) {
	tab := strings.TrimSpace(c.TableName)
	if tab == "" {
		tab = exportAllTab
	}
	sheetName, ok := sheetNames[tab]
	if !ok {
		return "", false
	}
	place := placements[tab]
	useScore := strings.EqualFold(c.Metric, domain.GradeMetricScore)
	ref := sheetRef(sheetName)

	var parts []string
	for _, contest := range contestsOfTab(std, tab) {
		p, ok := place[contest.ID]
		if !ok {
			continue
		}
		if p.totalOnly {
			// Колонка-сумма: число решённых (edu) или баллы (ioi).
			if p.isIoi == useScore {
				parts = append(parts, "N("+ref+xlsx.CellRef(p.firstCol, r)+")")
			}
			continue
		}
		rng := ref + xlsx.CellRef(p.firstCol, r) + ":" + ref + xlsx.CellRef(p.lastCol, r)
		if useScore {
			if p.isIoi {
				parts = append(parts, "SUM("+rng+")")
			}
		} else if !p.isIoi {
			parts = append(parts, "COUNTIF("+rng+",\"+\")")
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "+"), true
}

// normalizeRefFormula — знаменатель нормировки столбца: total — максимум задач
// вкладки (×100 для баллов); max — максимум сырой колонки по группе; число —
// как есть.
func normalizeRefFormula(c domain.GradeColumn, std domain.GeneratedGroupStandings, rawCol, firstRow, lastRow int) string {
	useScore := strings.EqualFold(c.Metric, domain.GradeMetricScore)
	switch c.Normalize.Mode {
	case domain.NormalizeFixed:
		return trimFloat(c.Normalize.Value)
	case domain.NormalizeTotal:
		tab := strings.TrimSpace(c.TableName)
		if tab == "" {
			tab = exportAllTab
		}
		count := 0
		for _, contest := range contestsOfTab(std, tab) {
			count += len(contest.Tasks)
		}
		if useScore {
			count *= 100
		}
		return strconv.Itoa(count)
	default: // max
		colRef := xlsx.ColName(rawCol)
		return "MAX(" + colRef + "$" + strconv.Itoa(firstRow+1) + ":" + colRef + "$" + strconv.Itoa(lastRow+1) + ")"
	}
}

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
