package providerbased

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"standings-edu/internal/domain"
)

const HTMLTableImportProviderID = "html_table_import"

type HTMLTableImportProvider struct {
	httpClient *http.Client
}

func NewHTMLTableImportProvider() *HTMLTableImportProvider {
	return &HTMLTableImportProvider{
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (p *HTMLTableImportProvider) ProviderID() string {
	return HTMLTableImportProviderID
}

type htmlTableImportConfig struct {
	PageURL          string   `json:"page_url"`
	Columns          []string `json:"columns"`
	AutoFind         bool     `json:"auto_find"`
	SearchSubstrings []string `json:"search_substrings,omitempty"`
}

type htmlImportColumnKind int

const (
	htmlColSkip htmlImportColumnKind = iota
	htmlColPlace
	htmlColName
	htmlColTask
	htmlColPenalty
)

type htmlTableImportSchema struct {
	columns      []htmlImportColumnKind
	nameIndex    int
	placeIndex   int
	penaltyIndex int
	taskIndices  []int
}

type parsedImportedRow struct {
	name     string
	place    string
	penalty  *int
	statuses []string
	scores   []*int
}

type parsedImportedTable struct {
	index int
	rows  []parsedImportedRow
}

type studentMatcher struct {
	student       domain.Student
	fullNameParts []string
	patterns      []string
}

type matchedStudentRow struct {
	row      parsedImportedRow
	matchLen int
	placeOrd int
	hasPlace bool
}

var (
	reTags  = regexp.MustCompile(`(?is)<[^>]+>`)
	reSpace = regexp.MustCompile(`\s+`)
	reInt   = regexp.MustCompile(`-?\d+`)
)

func (p *HTMLTableImportProvider) BuildStandings(ctx context.Context, input ProviderBuildInput) (domain.GeneratedContestStandings, error) {
	if p == nil || p.httpClient == nil {
		return domain.GeneratedContestStandings{}, fmt.Errorf("html table import provider client is not configured")
	}

	cfg, err := parseHTMLTableImportConfig(input.Contest.ProviderConfig)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	schema, err := parseHTMLTableImportSchema(cfg.Columns)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	pageHTML, err := p.fetchPage(ctx, cfg.PageURL)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	parsedTables, err := parseMatchingTables(pageHTML, schema)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	matchers := buildStudentMatchers(input.Students)
	extraSubstrings := normalizeSubstrings(cfg.SearchSubstrings)

	matchesByTable := make([]map[string]matchedStudentRow, 0, len(parsedTables))
	for _, table := range parsedTables {
		matchesByTable = append(matchesByTable, matchRowsToStudents(table.rows, matchers, cfg.AutoFind, extraSubstrings))
	}

	return buildImportedStandings(input.Contest, cfg.PageURL, schema, parsedTables, input.Students, matchesByTable), nil
}

func (p *HTMLTableImportProvider) fetchPage(ctx context.Context, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	res, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch page_url=%q: %w", pageURL, err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return "", fmt.Errorf("fetch page_url=%q status=%d body=%q", pageURL, res.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("read page_url=%q: %w", pageURL, err)
	}
	return string(body), nil
}

func parseHTMLTableImportConfig(raw json.RawMessage) (htmlTableImportConfig, error) {
	var cfg htmlTableImportConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return cfg, fmt.Errorf("provider_config is required for provider=%q", HTMLTableImportProviderID)
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode provider_config: %w", err)
	}

	cfg.PageURL = strings.TrimSpace(cfg.PageURL)
	if cfg.PageURL == "" {
		return cfg, fmt.Errorf("provider_config.page_url is required")
	}
	if len(cfg.Columns) == 0 {
		return cfg, fmt.Errorf("provider_config.columns must not be empty")
	}
	return cfg, nil
}

func parseHTMLTableImportSchema(columns []string) (htmlTableImportSchema, error) {
	schema := htmlTableImportSchema{
		columns:      make([]htmlImportColumnKind, len(columns)),
		nameIndex:    -1,
		placeIndex:   -1,
		penaltyIndex: -1,
		taskIndices:  make([]int, 0),
	}

	for i, col := range columns {
		kind := normalizeColumnKind(col)
		switch kind {
		case htmlColName:
			if schema.nameIndex >= 0 {
				return schema, fmt.Errorf("provider_config.columns contains duplicate name column")
			}
			schema.nameIndex = i
		case htmlColPlace:
			if schema.placeIndex >= 0 {
				return schema, fmt.Errorf("provider_config.columns contains duplicate place column")
			}
			schema.placeIndex = i
		case htmlColPenalty:
			if schema.penaltyIndex >= 0 {
				return schema, fmt.Errorf("provider_config.columns contains duplicate penalty column")
			}
			schema.penaltyIndex = i
		case htmlColTask:
			schema.taskIndices = append(schema.taskIndices, i)
		case htmlColSkip:
		default:
			return schema, fmt.Errorf("provider_config.columns[%d]=%q has unsupported kind", i, col)
		}
		schema.columns[i] = kind
	}

	if schema.nameIndex < 0 {
		return schema, fmt.Errorf("provider_config.columns must contain exactly one name column")
	}
	if len(schema.taskIndices) == 0 {
		return schema, fmt.Errorf("provider_config.columns must contain at least one task column")
	}
	return schema, nil
}

func normalizeColumnKind(raw string) htmlImportColumnKind {
	col := strings.ToLower(strings.TrimSpace(raw))
	switch col {
	case "place", "место":
		return htmlColPlace
	case "name", "имя", "участник":
		return htmlColName
	case "task", "задача":
		return htmlColTask
	case "penalty", "штраф":
		return htmlColPenalty
	case "skip", "пропустить":
		return htmlColSkip
	default:
		return -1
	}
}

func parseMatchingTables(pageHTML string, schema htmlTableImportSchema) ([]parsedImportedTable, error) {
	tableBlocks := extractTagBlocks(pageHTML, "table")
	for i, tableBlock := range tableBlocks {
		rows := extractTagBlocks(tableBlock, "tr")
		parsedRows := make([]parsedImportedRow, 0)
		for _, rowBlock := range rows {
			cells := extractCellTexts(rowBlock)
			if len(cells) != len(schema.columns) {
				continue
			}
			row := parseImportedRow(cells, schema)
			if strings.TrimSpace(row.name) == "" {
				continue
			}
			parsedRows = append(parsedRows, row)
		}
		if len(parsedRows) > 0 {
			return []parsedImportedTable{
				{
					index: i + 1,
					rows:  parsedRows,
				},
			}, nil
		}
	}

	return nil, fmt.Errorf("no matching table found on page with row column count=%d", len(schema.columns))
}

func parseImportedRow(cells []string, schema htmlTableImportSchema) parsedImportedRow {
	row := parsedImportedRow{
		statuses: make([]string, len(schema.taskIndices)),
		scores:   make([]*int, len(schema.taskIndices)),
	}
	for i := range row.statuses {
		row.statuses[i] = domain.TaskStatusNone
	}

	taskPos := 0
	for colIdx, kind := range schema.columns {
		cell := normalizeCellText(cells[colIdx])
		switch kind {
		case htmlColName:
			row.name = cell
		case htmlColPlace:
			row.place = cell
		case htmlColPenalty:
			if penalty, ok := parseInt(cell); ok {
				value := penalty
				row.penalty = &value
			}
		case htmlColTask:
			status, score := parseTaskCell(cell)
			row.statuses[taskPos] = status
			if score != nil {
				value := *score
				row.scores[taskPos] = &value
			}
			taskPos++
		}
	}

	return row
}

func parseTaskCell(cell string) (string, *int) {
	if cell == "" || cell == "." || cell == "-" {
		return domain.TaskStatusNone, nil
	}

	if strings.Contains(cell, "+") {
		v := 100
		return domain.TaskStatusSolved, &v
	}

	if score, ok := parseInt(cell); ok {
		value := score
		if value < 0 {
			value = 0
		}
		if value >= 100 {
			return domain.TaskStatusSolved, &value
		}
		return domain.TaskStatusAttempted, &value
	}

	if strings.Contains(strings.ToLower(cell), "ok") {
		v := 100
		return domain.TaskStatusSolved, &v
	}

	v := 0
	return domain.TaskStatusAttempted, &v
}

func buildStudentMatchers(students []domain.Student) []studentMatcher {
	out := make([]studentMatcher, 0, len(students))
	for _, s := range students {
		full := normalizeForMatch(s.FullName)
		public := normalizeForMatch(s.PublicName)

		patterns := make([]string, 0, 6)
		if full != "" {
			patterns = append(patterns, full)
		}
		if public != "" && public != full {
			patterns = append(patterns, public)
		}
		patterns = append(patterns, buildInitialPatterns(s.FullName)...)
		patterns = uniqueStrings(patterns)

		fullParts := make([]string, 0, 2)
		if full != "" {
			fullParts = append(fullParts, full)
		}
		fullParts = append(fullParts, buildInitialPatterns(s.FullName)...)
		fullParts = uniqueStrings(fullParts)

		out = append(out, studentMatcher{
			student:       s,
			fullNameParts: fullParts,
			patterns:      patterns,
		})
	}
	return out
}

func buildInitialPatterns(fullName string) []string {
	tokens := strings.Fields(normalizeForMatch(fullName))
	if len(tokens) < 3 {
		return nil
	}

	last := tokens[0]
	name := firstRune(tokens[1])
	patronymic := firstRune(tokens[2])
	if name == "" || patronymic == "" || last == "" {
		return nil
	}

	return []string{
		last + " " + name + ". " + patronymic + ".",
		last + " " + name + "." + patronymic + ".",
		name + ". " + patronymic + ". " + last,
	}
}

func normalizeSubstrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		s := normalizeForMatch(item)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return uniqueStrings(out)
}

func matchRowsToStudents(rows []parsedImportedRow, matchers []studentMatcher, autoFind bool, extraSubstrings []string) map[string]matchedStudentRow {
	matched := make(map[string]matchedStudentRow)
	for _, row := range rows {
		nameNorm := normalizeForMatch(row.name)
		if nameNorm == "" {
			continue
		}

		if autoFind && !passesAutoFind(nameNorm, matchers, extraSubstrings) {
			continue
		}

		bestStudentID := ""
		bestLen := 0
		for _, matcher := range matchers {
			cur := bestPatternLength(nameNorm, matcher.patterns)
			if cur > bestLen {
				bestLen = cur
				bestStudentID = matcher.student.ID
			}
		}
		if bestStudentID == "" {
			continue
		}

		placeOrd, hasPlace := parsePlaceOrder(row.place)
		existing, ok := matched[bestStudentID]
		if !ok || bestLen > existing.matchLen || (bestLen == existing.matchLen && comparePlaceOrder(placeOrd, hasPlace, existing.placeOrd, existing.hasPlace) < 0) {
			matched[bestStudentID] = matchedStudentRow{
				row:      row,
				matchLen: bestLen,
				placeOrd: placeOrd,
				hasPlace: hasPlace,
			}
		}
	}
	return matched
}

func passesAutoFind(nameNorm string, matchers []studentMatcher, extraSubstrings []string) bool {
	for _, matcher := range matchers {
		for _, part := range matcher.fullNameParts {
			if part != "" && strings.Contains(nameNorm, part) {
				return true
			}
		}
	}
	for _, part := range extraSubstrings {
		if strings.Contains(nameNorm, part) {
			return true
		}
	}
	return false
}

func bestPatternLength(nameNorm string, patterns []string) int {
	best := 0
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.Contains(nameNorm, p) && len(p) > best {
			best = len(p)
		}
	}
	return best
}

func buildImportedStandings(
	contest domain.Contest,
	pageURL string,
	schema htmlTableImportSchema,
	tables []parsedImportedTable,
	students []domain.Student,
	matchesByTable []map[string]matchedStudentRow,
) domain.GeneratedContestStandings {
	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		Olympiad:    contest.Olympiad,
		Subcontests: make([]domain.GeneratedSubcontest, 0, len(tables)),
		Tasks:       make([]domain.GeneratedTask, 0),
		Rows:        make([]domain.GeneratedRow, 0, len(students)),
	}

	tableTaskOffsets := make([]int, len(tables))
	totalTasks := 0
	for i, table := range tables {
		tableTaskOffsets[i] = totalTasks

		subTasks := make([]domain.GeneratedTask, 0, len(schema.taskIndices))
		for j := range schema.taskIndices {
			url := fmt.Sprintf("%s#table-%d-task-%d", pageURL, table.index, j+1)
			subTasks = append(subTasks, domain.GeneratedTask{
				Label:         htmlImportAlphabetLabel(j),
				URL:           url,
				NormalizedURL: domain.NormalizeTaskURL(url),
			})
		}

		out.Subcontests = append(out.Subcontests, domain.GeneratedSubcontest{
			Title:     "Результаты",
			TaskCount: len(subTasks),
			Tasks:     subTasks,
		})
		out.Tasks = append(out.Tasks, subTasks...)
		totalTasks += len(subTasks)
	}

	for _, student := range students {
		row := domain.GeneratedRow{
			StudentID:   student.ID,
			PublicName:  student.PublicName,
			Statuses:    make([]string, totalTasks),
			SolvedCount: 0,
		}
		for i := range row.Statuses {
			row.Statuses[i] = domain.TaskStatusNone
		}
		if contest.Olympiad {
			row.Scores = make([]*int, totalTasks)
		}

		for tableIdx := range tables {
			match := matchesByTable[tableIdx][student.ID]
			if match.matchLen == 0 {
				continue
			}
			if row.Place == "" && strings.TrimSpace(match.row.place) != "" {
				row.Place = strings.TrimSpace(match.row.place)
			}
			if row.Penalty == nil && match.row.penalty != nil {
				p := *match.row.penalty
				row.Penalty = &p
			}

			offset := tableTaskOffsets[tableIdx]
			for i := range match.row.statuses {
				status := match.row.statuses[i]
				row.Statuses[offset+i] = status
				if status == domain.TaskStatusSolved {
					row.SolvedCount++
				}
				if contest.Olympiad && match.row.scores[i] != nil {
					val := *match.row.scores[i]
					row.Scores[offset+i] = &val
					row.TotalScore += val
				}
			}
		}

		out.Rows = append(out.Rows, row)
	}

	sortProviderRows(out.Rows, contest.Olympiad)
	return out
}

func sortProviderRows(rows []domain.GeneratedRow, olympiad bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		iOrd, iHas := parsePlaceOrder(rows[i].Place)
		jOrd, jHas := parsePlaceOrder(rows[j].Place)
		if c := comparePlaceOrder(iOrd, iHas, jOrd, jHas); c != 0 {
			return c < 0
		}

		if olympiad {
			if rows[i].TotalScore != rows[j].TotalScore {
				return rows[i].TotalScore > rows[j].TotalScore
			}
		} else if rows[i].SolvedCount != rows[j].SolvedCount {
			return rows[i].SolvedCount > rows[j].SolvedCount
		}

		return strings.ToLower(rows[i].PublicName) < strings.ToLower(rows[j].PublicName)
	})
}

func comparePlaceOrder(left int, leftHas bool, right int, rightHas bool) int {
	if leftHas && !rightHas {
		return -1
	}
	if !leftHas && rightHas {
		return 1
	}
	if leftHas && rightHas {
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		}
	}
	return 0
}

func parsePlaceOrder(place string) (int, bool) {
	v, ok := parseInt(place)
	if !ok || v <= 0 {
		return 0, false
	}
	return v, true
}

func parseInt(s string) (int, bool) {
	match := reInt.FindString(normalizeCellText(s))
	if match == "" {
		return 0, false
	}
	v, err := strconv.Atoi(match)
	if err != nil {
		return 0, false
	}
	return v, true
}

func extractTagBlocks(src string, tag string) []string {
	lower := strings.ToLower(src)
	openTag := "<" + strings.ToLower(tag)
	closeTag := "</" + strings.ToLower(tag)

	type stackItem struct {
		start int
	}
	stack := make([]stackItem, 0)
	out := make([]string, 0)

	i := 0
	for i < len(lower) {
		openIdx := strings.Index(lower[i:], openTag)
		if openIdx >= 0 {
			openIdx += i
		}
		closeIdx := strings.Index(lower[i:], closeTag)
		if closeIdx >= 0 {
			closeIdx += i
		}

		if openIdx < 0 && closeIdx < 0 {
			break
		}

		if openIdx >= 0 && (closeIdx < 0 || openIdx < closeIdx) {
			openEnd := strings.Index(lower[openIdx:], ">")
			if openEnd < 0 {
				break
			}
			stack = append(stack, stackItem{start: openIdx})
			i = openIdx + openEnd + 1
			continue
		}

		closeEnd := strings.Index(lower[closeIdx:], ">")
		if closeEnd < 0 {
			break
		}
		end := closeIdx + closeEnd + 1

		if len(stack) > 0 {
			start := stack[len(stack)-1].start
			stack = stack[:len(stack)-1]
			if start >= 0 && end <= len(src) && start < end {
				out = append(out, src[start:end])
			}
		}
		i = end
	}

	return out
}

func extractCellTexts(rowBlock string) []string {
	lower := strings.ToLower(rowBlock)
	out := make([]string, 0)

	i := 0
	for i < len(lower) {
		tdIdx := strings.Index(lower[i:], "<td")
		if tdIdx >= 0 {
			tdIdx += i
		}
		thIdx := strings.Index(lower[i:], "<th")
		if thIdx >= 0 {
			thIdx += i
		}

		if tdIdx < 0 && thIdx < 0 {
			break
		}

		tag := "td"
		start := tdIdx
		if thIdx >= 0 && (tdIdx < 0 || thIdx < tdIdx) {
			tag = "th"
			start = thIdx
		}

		openEndRel := strings.Index(lower[start:], ">")
		if openEndRel < 0 {
			break
		}
		contentStart := start + openEndRel + 1

		closeIdx := strings.Index(lower[contentStart:], "</"+tag+">")
		if closeIdx < 0 {
			break
		}
		contentEnd := contentStart + closeIdx
		out = append(out, normalizeCellText(rowBlock[contentStart:contentEnd]))
		i = contentEnd + len("</"+tag+">")
	}

	return out
}

func normalizeCellText(s string) string {
	text := reTags.ReplaceAllString(s, " ")
	text = html.UnescapeString(text)
	text = strings.ReplaceAll(text, "\u00a0", " ")
	text = reSpace.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func normalizeForMatch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = reSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func firstRune(s string) string {
	for _, r := range s {
		return strings.ToLower(string(r))
	}
	return ""
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func htmlImportAlphabetLabel(idx int) string {
	if idx < 0 {
		return ""
	}
	label := ""
	for idx >= 0 {
		label = string(rune('A'+(idx%26))) + label
		idx = idx/26 - 1
	}
	return label
}
