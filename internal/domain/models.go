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
	Subcontests    []Subcontest      `json:"subcontests"`
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
	Title      string   `json:"title"`
	FormLink   string   `json:"form_link,omitempty"`
	Update     *bool    `json:"update,omitempty"`
	StudentIDs []string `json:"student_ids"`
}

type GroupDefinition struct {
	Slug       string
	Title      string
	FormLink   string
	Update     bool
	StudentIDs []string
	Contests   []GroupContestRef
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
	Scores         []*int   `json:"scores,omitempty"`
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
	GeneratedAt *time.Time            `json:"generated_at,omitempty"`
	Subcontests []GeneratedSubcontest `json:"subcontests"`
	Tasks       []GeneratedTask       `json:"tasks"`
	Rows        []GeneratedRow        `json:"rows"`
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
		Subcontests []GeneratedSubcontest `json:"subcontests"`
		Tasks       []GeneratedTask       `json:"tasks"`
		Rows        []GeneratedRow        `json:"rows"`
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
		Subcontests: raw.Subcontests,
		Tasks:       raw.Tasks,
		Rows:        raw.Rows,
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
	Contests           []GeneratedContestStandings      `json:"contests"`
}

type GeneratedGroupSolvedSummaryRow struct {
	StudentID              string `json:"student_id"`
	PublicName             string `json:"public_name"`
	SolvedCountOnPageSites int    `json:"solved_count_on_page_sites"`
	TotalSolvedCount       int    `json:"total_solved_count"`
	SolvedCountBySite      []int  `json:"solved_count_by_site,omitempty"`
}
