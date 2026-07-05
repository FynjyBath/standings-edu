package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Account struct {
	Site      string `json:"site"`
	AccountID string `json:"account_id"`
}

type Student struct {
	FullName   string    `json:"full_name"`
	ID         string    `json:"id"`
	PublicName string    `json:"public_name"`
	Accounts   []Account `json:"accounts"`
	Groups     []string  `json:"groups,omitempty"`
}

type Subcontest struct {
	Title string   `json:"title"`
	Tasks []string `json:"tasks"`
}

type ContestMaterial struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type ScoreSystem string

const (
	ScoreSystemEdu ScoreSystem = "edu"
	ScoreSystemIOI ScoreSystem = "ioi"
)

func (m ScoreSystem) Normalized() ScoreSystem {
	switch strings.ToLower(strings.TrimSpace(string(m))) {
	case "", string(ScoreSystemEdu):
		return ScoreSystemEdu
	case string(ScoreSystemIOI):
		return ScoreSystemIOI
	default:
		return ScoreSystemEdu
	}
}

func (m ScoreSystem) IsIOI() bool {
	return m.Normalized() == ScoreSystemIOI
}

func (m ScoreSystem) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(m.Normalized()))
}

func (m *ScoreSystem) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		switch strings.ToLower(strings.TrimSpace(asString)) {
		case "", string(ScoreSystemEdu):
			*m = ScoreSystemEdu
			return nil
		case string(ScoreSystemIOI):
			*m = ScoreSystemIOI
			return nil
		default:
			return fmt.Errorf("score_system must be %q or %q", ScoreSystemEdu, ScoreSystemIOI)
		}
	}
	return fmt.Errorf("score_system must be string (%q/%q)", ScoreSystemEdu, ScoreSystemIOI)
}

// TableNameList — список вкладок сводной таблицы, в которые входит контест.
// Принимает и одну строку ("table_name": "Пробники"), и массив строк.
type TableNameList []string

func (t *TableNameList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*t = nil
		return nil
	}

	var arr []string
	if err := json.Unmarshal(trimmed, &arr); err == nil {
		*t = NormalizeTableNames(arr)
		return nil
	}

	var single string
	if err := json.Unmarshal(trimmed, &single); err == nil {
		*t = NormalizeTableNames([]string{single})
		return nil
	}

	return fmt.Errorf("table_name must be a string or an array of strings")
}

// NormalizeTableNames убирает пустые/пробельные имена и дубликаты, сохраняя порядок.
func NormalizeTableNames(names []string) TableNameList {
	if len(names) == 0 {
		return nil
	}
	out := make(TableNameList, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = NormalizeWhitespace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type Contest struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	ScoreSystem    ScoreSystem       `json:"score_system"`
	ContestType    string            `json:"source_type,omitempty"`
	TableNames     TableNameList     `json:"table_name,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	ProviderConfig json.RawMessage   `json:"provider_config,omitempty"`
	Materials      []ContestMaterial `json:"materials,omitempty"`
	// StartTime/EndTime — окно tasks-контеста (ISO 8601 с явным сдвигом, напр.
	// "2026-09-01T18:00:00+03:00"). Если заданы, для сайтов с временем посылок
	// в зачёт идут только посылки в окне, остальное после конца — в дорешку.
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	// FreezeTime — вычисленный момент заморозки (из записи группы). Не хранится
	// в определении контеста: проставляется при резолве контеста группы.
	FreezeTime *time.Time `json:"-"`
	// ZeroPenalty — штраф (в баллах) за каждую задачу без баллов: пустую или с
	// нулём. Действует только при score_system=ioi; 0 — выключено.
	ZeroPenalty int          `json:"zero_penalty,omitempty"`
	Subcontests []Subcontest `json:"subcontests"`
}

func (c *Contest) UnmarshalJSON(data []byte) error {
	type rawContest struct {
		ID             string            `json:"id"`
		Title          string            `json:"title"`
		ScoreSystem    *ScoreSystem      `json:"score_system"`
		SourceType     *string           `json:"source_type,omitempty"`
		ContestType    *string           `json:"contest_type,omitempty"` // legacy alias
		TableNames     TableNameList     `json:"table_name,omitempty"`
		Provider       string            `json:"provider,omitempty"`
		ProviderConfig json.RawMessage   `json:"provider_config,omitempty"`
		Materials      []ContestMaterial `json:"materials,omitempty"`
		StartTime      *time.Time        `json:"start_time,omitempty"`
		EndTime        *time.Time        `json:"end_time,omitempty"`
		ZeroPenalty    int               `json:"zero_penalty,omitempty"`
		Subcontests    []Subcontest      `json:"subcontests"`
	}

	var raw rawContest
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = Contest{
		ID:             raw.ID,
		Title:          raw.Title,
		ContestType:    resolveSourceType(raw.SourceType, raw.ContestType),
		TableNames:     raw.TableNames,
		Provider:       raw.Provider,
		ProviderConfig: raw.ProviderConfig,
		Materials:      raw.Materials,
		StartTime:      raw.StartTime,
		EndTime:        raw.EndTime,
		ZeroPenalty:    raw.ZeroPenalty,
		Subcontests:    raw.Subcontests,
		ScoreSystem:    ScoreSystemEdu,
	}
	if raw.ScoreSystem != nil {
		c.ScoreSystem = raw.ScoreSystem.Normalized()
	}
	return nil
}

// resolveSourceType предпочитает новое поле source_type, но принимает старое
// contest_type для обратной совместимости.
func resolveSourceType(sourceType, contestType *string) string {
	if sourceType != nil {
		return *sourceType
	}
	if contestType != nil {
		return *contestType
	}
	return ""
}

const (
	ContestTypeTasks    = "tasks"
	ContestTypeProvider = "provider"
)

func (c Contest) TypeOrDefault() string {
	typ := strings.ToLower(strings.TrimSpace(c.ContestType))
	if typ == "" {
		return ContestTypeTasks
	}
	return typ
}

func NormalizeContestMaterials(materials []ContestMaterial) []ContestMaterial {
	if len(materials) == 0 {
		return nil
	}

	out := make([]ContestMaterial, 0, len(materials))
	for _, material := range materials {
		url := strings.TrimSpace(material.URL)
		if url == "" {
			continue
		}

		title := strings.TrimSpace(material.Title)
		if title == "" {
			title = url
		}

		out = append(out, ContestMaterial{
			Title: title,
			URL:   url,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type GroupFile struct {
	Title      string        `json:"title"`
	FormLink   string        `json:"form_link,omitempty"`
	Update     *bool         `json:"update,omitempty"`
	StudentIDs []string      `json:"student_ids"`
	Grades     *GradesConfig `json:"grades,omitempty"`
	// GroupSecretToken — секрет для просмотра размороженных таблиц
	// (?token=… на страницах группы). Пусто — токенного доступа нет.
	GroupSecretToken string `json:"group_secret_token,omitempty"`
}

type GroupDefinition struct {
	Slug       string
	Title      string
	FormLink   string
	Update     bool
	StudentIDs []string
	Contests   []GroupContestRef
	Grades     *GradesConfig
}

// GradesConfig — описание таблицы оценок группы (из group.json).
type GradesConfig struct {
	Title   string        `json:"title,omitempty"`
	Round   *int          `json:"round,omitempty"` // знаков после запятой у итога; nil = 1
	Columns []GradeColumn `json:"columns"`
}

type GradeColumn struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Weight    float64       `json:"weight"`
	Type      string        `json:"type"`                 // "table" | "manual"
	TableName string        `json:"table_name,omitempty"` // для type=table; пусто = все контесты
	Metric    string        `json:"metric,omitempty"`     // "plus" | "score"
	Normalize NormalizeSpec `json:"normalize,omitempty"`  // "max" | "total" | число
	// Upsolving — коэффициент учёта дорешки (0..1): вклад задачи =
	// max(основной, дорешка×коэффициент). nil — старое поведение (дорешка
	// на полную, как основной результат).
	Upsolving *float64 `json:"upsolving,omitempty"`
}

const (
	GradeColumnTable  = "table"
	GradeColumnManual = "manual"
	GradeMetricPlus   = "plus"
	GradeMetricScore  = "score"

	NormalizeMax   = "max"
	NormalizeTotal = "total"
	NormalizeFixed = "fixed"
)

// NormalizeSpec — способ нормировки табличной оценки: "max", "total" или число.
type NormalizeSpec struct {
	Mode  string
	Value float64
}

// MarshalJSON пишет normalize обратно в формат group.json ("max"/"total"/число).
// Без него перезапись group.json (intake, админка) превращала поле в объект
// {"Mode":...}, который UnmarshalJSON затем не принимал — группа «ломалась».
func (n NormalizeSpec) MarshalJSON() ([]byte, error) {
	switch n.Mode {
	case NormalizeTotal:
		return json.Marshal(NormalizeTotal)
	case NormalizeFixed:
		return json.Marshal(n.Value)
	default: // "" и "max" — режим по умолчанию
		return json.Marshal(NormalizeMax)
	}
}

func (n *NormalizeSpec) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		n.Mode = NormalizeMax
		return nil
	}

	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "", NormalizeMax:
			n.Mode = NormalizeMax
		case NormalizeTotal:
			n.Mode = NormalizeTotal
		default:
			return fmt.Errorf("normalize must be %q, %q or a number", NormalizeMax, NormalizeTotal)
		}
		return nil
	}

	var f float64
	if err := json.Unmarshal(trimmed, &f); err == nil {
		n.Mode = NormalizeFixed
		n.Value = f
		return nil
	}

	return fmt.Errorf("normalize must be %q, %q or a number", NormalizeMax, NormalizeTotal)
}

type GroupContestRef struct {
	ID     string
	Update bool
	// Inline — необязательное полное определение контеста прямо в файле группы.
	// Если задано, контест берётся отсюда и не обязан присутствовать в
	// глобальном data/contests.json (удобно для разовых контестов).
	Inline *Contest
	// TableNames — переопределение вкладок сводной таблицы для этой группы.
	// Позволяет указать table_name даже для ссылки на глобальный контест.
	// nil — использовать table_name из определения контеста.
	TableNames TableNameList
	// StartTime/EndTime — окно контеста, заданное на стороне группы (в записи
	// groups/<slug>/contests.json). Непустое значение переопределяет окно из
	// определения контеста. nil — оставить как в определении.
	StartTime *time.Time
	EndTime   *time.Time
	// Freeze — заморозка результатов (поле "freeze" записи группы): в публичную
	// таблицу попадают только посылки до момента заморозки. nil — заморозки нет.
	Freeze *FreezeSpec
}

// FreezeSpec — параметр заморозки: либо всё соревнование ("all"), либо
// длительность от конца окна (напр. "1h" → последний час скрыт).
type FreezeSpec struct {
	All      bool
	Duration time.Duration
}

// ParseFreezeSpec разбирает поле "freeze": "" → nil (нет заморозки), "all" →
// всё соревнование, иначе — Go-длительность ("30m", "1h", "1h30m") строго > 0.
func ParseFreezeSpec(raw string) (*FreezeSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.EqualFold(raw, "all") {
		return &FreezeSpec{All: true}, nil
	}
	dur, err := time.ParseDuration(raw)
	if err != nil || dur <= 0 {
		return nil, fmt.Errorf(`freeze: ожидается "all" или длительность > 0 (напр. "1h", "30m", "1h30m")`)
	}
	return &FreezeSpec{Duration: dur}, nil
}

// FreezeMoment возвращает момент заморозки для окна [start, end]: для "all" —
// начало окна, иначе end−Duration (не раньше начала). Без полного окна — nil,
// заморозке не от чего отсчитываться.
func (f *FreezeSpec) FreezeMoment(start, end *time.Time) *time.Time {
	if f == nil || start == nil || end == nil || end.Before(*start) {
		return nil
	}
	moment := *start
	if !f.All {
		moment = end.Add(-f.Duration)
		if moment.Before(*start) {
			moment = *start
		}
	}
	return &moment
}

type SourceData struct {
	Students map[string]Student
	Contests map[string]Contest
	Groups   []GroupDefinition
}

const (
	TaskStatusSolved    = "solved"
	TaskStatusAttempted = "attempted"
	TaskStatusNone      = "none"
)

type GeneratedGroupMeta struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type GeneratedTask struct {
	Label         string `json:"label"`
	URL           string `json:"url"`
	NormalizedURL string `json:"normalized_url"`
}

type GeneratedSubcontest struct {
	Title     string          `json:"title"`
	TaskCount int             `json:"task_count"`
	Tasks     []GeneratedTask `json:"tasks"`
}

type GeneratedRow struct {
	StudentID      string   `json:"student_id"`
	PublicName     string   `json:"public_name"`
	Place          string   `json:"place,omitempty"`
	Penalty        *int     `json:"penalty,omitempty"`
	ProviderStatus string   `json:"provider_status,omitempty"`
	SolvedCount    int      `json:"solved_count"`
	TotalScore     int      `json:"total_score,omitempty"`
	Statuses       []string `json:"statuses"`
	// Scores — баллы в основное время (в окне контеста); без окна — общие.
	Scores []*int `json:"scores,omitempty"`
	// PracticeScores[i] — балл в дорешке, если он строго больше основного
	// (показывается в скобках: «50 (70)»). nil/элемент nil — дорешки нет.
	PracticeScores []*int `json:"practice_scores,omitempty"`
	// Upsolved[i] == true означает, что задача i решена/попытана только в
	// дорешке (после контеста). Такие ячейки показываются в скобках и не влияют
	// на место/штраф. Пустой/nil — дорешки нет.
	Upsolved []bool `json:"upsolved,omitempty"`
}

type GeneratedContestStandings struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	ScoreSystem ScoreSystem       `json:"score_system"`
	ContestType string            `json:"source_type,omitempty"`
	TableNames  TableNameList     `json:"table_name,omitempty"`
	Materials   []ContestMaterial `json:"materials,omitempty"`
	// GeneratedAt — момент последней генерации именно этой таблицы. У контестов
	// с update=false он остаётся старым, поэтому видно, что таблица давно не
	// обновлялась. nil — для таблиц, сгенерированных до появления этого поля.
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	// StartTime/EndTime — окно контеста (из определения или записи группы).
	// До StartTime сервер не отдаёт ссылки на задачи; окно показывается в шапке.
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	// ZeroPenalty — применённый при сборке штраф за задачу без баллов (ioi).
	ZeroPenalty int `json:"zero_penalty,omitempty"`
	// FrozenAt — таблица заморожена: в неё вошли только посылки до этого момента.
	// nil — таблица полная.
	FrozenAt    *time.Time            `json:"frozen_at,omitempty"`
	Subcontests []GeneratedSubcontest `json:"subcontests"`
	Tasks       []GeneratedTask       `json:"tasks"`
	Rows        []GeneratedRow        `json:"rows"`
	// RowsFull — полные строки замороженного контеста (для просмотра по токену
	// группы). Сервер вырезает их из публичных ответов. nil — контест не заморожен.
	RowsFull []GeneratedRow `json:"rows_full,omitempty"`
}

func (c *GeneratedContestStandings) UnmarshalJSON(data []byte) error {
	type rawGeneratedContest struct {
		ID          string                `json:"id"`
		Title       string                `json:"title"`
		ScoreSystem *ScoreSystem          `json:"score_system"`
		SourceType  *string               `json:"source_type,omitempty"`
		ContestType *string               `json:"contest_type,omitempty"` // legacy alias
		TableNames  TableNameList         `json:"table_name,omitempty"`
		Materials   []ContestMaterial     `json:"materials,omitempty"`
		GeneratedAt *time.Time            `json:"generated_at,omitempty"`
		StartTime   *time.Time            `json:"start_time,omitempty"`
		EndTime     *time.Time            `json:"end_time,omitempty"`
		ZeroPenalty int                   `json:"zero_penalty,omitempty"`
		FrozenAt    *time.Time            `json:"frozen_at,omitempty"`
		Subcontests []GeneratedSubcontest `json:"subcontests"`
		Tasks       []GeneratedTask       `json:"tasks"`
		Rows        []GeneratedRow        `json:"rows"`
		RowsFull    []GeneratedRow        `json:"rows_full,omitempty"`
	}

	var raw rawGeneratedContest
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = GeneratedContestStandings{
		ID:          raw.ID,
		Title:       raw.Title,
		ContestType: resolveSourceType(raw.SourceType, raw.ContestType),
		TableNames:  raw.TableNames,
		Materials:   raw.Materials,
		GeneratedAt: raw.GeneratedAt,
		StartTime:   raw.StartTime,
		EndTime:     raw.EndTime,
		ZeroPenalty: raw.ZeroPenalty,
		FrozenAt:    raw.FrozenAt,
		Subcontests: raw.Subcontests,
		Tasks:       raw.Tasks,
		Rows:        raw.Rows,
		RowsFull:    raw.RowsFull,
		ScoreSystem: ScoreSystemEdu,
	}
	if raw.ScoreSystem != nil {
		c.ScoreSystem = raw.ScoreSystem.Normalized()
	}
	return nil
}

type GeneratedGroupStandings struct {
	GroupSlug          string                           `json:"group_slug"`
	GroupTitle         string                           `json:"group_title"`
	FormLink           string                           `json:"form_link,omitempty"`
	SolvedSummarySites []string                         `json:"solved_summary_sites,omitempty"`
	SolvedSummary      []GeneratedGroupSolvedSummaryRow `json:"solved_summary,omitempty"`
	Grades             *GeneratedGrades                 `json:"grades,omitempty"`
	// GradesFull — оценки по полным (незамороженным) таблицам; есть только
	// когда в группе есть замороженные контесты. Сервер отдаёт их вместо Grades
	// при просмотре по токену и вырезает из публичных ответов.
	GradesFull *GeneratedGrades            `json:"grades_full,omitempty"`
	Contests   []GeneratedContestStandings `json:"contests"`
}

// SwapInFullRows подменяет строки замороженных контестов полными (rows_full) и
// оценки — полными (grades_full), убирая full-варианты из структуры. FrozenAt
// сохраняется, чтобы было видно, что публичная версия отличается. Возвращает
// true, если была хоть одна подмена (просмотр по токену).
func (s *GeneratedGroupStandings) SwapInFullRows() bool {
	swapped := false
	for i := range s.Contests {
		if s.Contests[i].RowsFull != nil {
			s.Contests[i].Rows = s.Contests[i].RowsFull
			s.Contests[i].RowsFull = nil
			swapped = true
		}
	}
	if s.GradesFull != nil {
		s.Grades = s.GradesFull
		s.GradesFull = nil
		swapped = true
	}
	return swapped
}

// StripFullRows убирает полные варианты из публичного ответа, чтобы
// размороженные данные не утекали без токена.
func (s *GeneratedGroupStandings) StripFullRows() {
	for i := range s.Contests {
		s.Contests[i].RowsFull = nil
	}
	s.GradesFull = nil
}

// GeneratedGrades — готовая таблица оценок (считается в generate, рендерится сервером).
type GeneratedGrades struct {
	Title   string                 `json:"title,omitempty"`
	Columns []GeneratedGradeColumn `json:"columns"`
	Rows    []GeneratedGradeRow    `json:"rows"`
}

type GeneratedGradeColumn struct {
	Title  string  `json:"title"`
	Weight float64 `json:"weight"`
}

type GeneratedGradeRow struct {
	StudentID  string `json:"student_id"`
	PublicName string `json:"public_name"`
	// Values — по одному значению на столбец; nil = нет оценки (в среднее не идёт).
	Values []*float64 `json:"values"`
	// Final — итоговое взвешенное среднее; nil, если ни одной оценки нет.
	Final *float64 `json:"final,omitempty"`
}

type GeneratedGroupSolvedSummaryRow struct {
	StudentID              string `json:"student_id"`
	PublicName             string `json:"public_name"`
	SolvedCountOnPageSites int    `json:"solved_count_on_page_sites"`
	TotalSolvedCount       int    `json:"total_solved_count"`
	SolvedCountBySite      []int  `json:"solved_count_by_site,omitempty"`
}
