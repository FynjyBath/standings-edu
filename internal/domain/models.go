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
	ID    string `json:"id"`
	Title string `json:"title"`
	// ShortName — краткое название для узких мест (колонка «только сумма» в
	// сводной). Пусто — используется Title.
	ShortName      string            `json:"short_name,omitempty"`
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
	ZeroPenalty int `json:"zero_penalty,omitempty"`
	// SummaryTotalOnly — в сводной таблице показывать контест одной колонкой
	// суммы (без детализации по задачам). Страница группы не меняется.
	SummaryTotalOnly bool `json:"summary_total_only,omitempty"`
	// Hidden — скрыть контест на страницах для школьников (список и сводная).
	// Он по-прежнему считается (в т.ч. в оценках) и виден жюри по токену.
	// false (по умолчанию, поле опущено) — контест виден.
	Hidden bool `json:"hidden,omitempty"`
	// Freeze — заморозка по умолчанию для всех групп, подключивших контест
	// по ссылке ("1h"/"all"). Запись группы может переопределить (в т.ч.
	// "none" — выключить). Работает при заданном окне.
	Freeze      string       `json:"freeze,omitempty"`
	Subcontests []Subcontest `json:"subcontests"`
}

func (c *Contest) UnmarshalJSON(data []byte) error {
	type rawContest struct {
		ID             string            `json:"id"`
		Title          string            `json:"title"`
		ShortName      string            `json:"short_name,omitempty"`
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
		SummaryTotal   bool              `json:"summary_total_only,omitempty"`
		Hidden         bool              `json:"hidden,omitempty"`
		Freeze         string            `json:"freeze,omitempty"`
		Subcontests    []Subcontest      `json:"subcontests"`
	}

	var raw rawContest
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = Contest{
		ID:               raw.ID,
		Title:            raw.Title,
		ShortName:        raw.ShortName,
		ContestType:      resolveSourceType(raw.SourceType, raw.ContestType),
		TableNames:       raw.TableNames,
		Provider:         raw.Provider,
		ProviderConfig:   raw.ProviderConfig,
		Materials:        raw.Materials,
		StartTime:        raw.StartTime,
		EndTime:          raw.EndTime,
		ZeroPenalty:      raw.ZeroPenalty,
		SummaryTotalOnly: raw.SummaryTotal,
		Hidden:           raw.Hidden,
		Freeze:           raw.Freeze,
		Subcontests:      raw.Subcontests,
		ScoreSystem:      ScoreSystemEdu,
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
	Title string `json:"title"`
	// ShortName — короткое название группы (для тесных мест). В объединённой
	// группе им подписываются участники: «Имя (короткое)».
	ShortName  string        `json:"short_name,omitempty"`
	FormLink   string        `json:"form_link,omitempty"`
	Update     *bool         `json:"update,omitempty"`
	StudentIDs []string      `json:"student_ids"`
	Grades     *GradesConfig `json:"grades,omitempty"`
	// GroupSecretToken — секрет для просмотра размороженных таблиц
	// (?token=… на страницах группы). Пусто — токенного доступа нет.
	GroupSecretToken string `json:"group_secret_token,omitempty"`
	// MemberGroups — если непусто, это «объединённая группа»: её страница
	// собирается на лету из таблиц перечисленных групп (слаги). Свои контесты и
	// ученики у неё не используются.
	MemberGroups []string `json:"member_groups,omitempty"`
	// HiddenContests — id контестов, скрытых в объединённой группе (галочка
	// «показывать» в её настройках). Только для объединённых групп.
	HiddenContests []string `json:"hidden_contests,omitempty"`
	// ShowTaskLinks — показывать ли ссылки на задачи на странице, видной
	// ученикам. nil/absent или true — показывать (как раньше); false — на
	// публичной странице ссылок на задачи нет (по токену жюри видны всегда).
	ShowTaskLinks *bool `json:"show_task_links,omitempty"`
}

// TaskLinksShown — показывать ли ссылки на задачи (по умолчанию да).
func (g GroupFile) TaskLinksShown() bool {
	return g.ShowTaskLinks == nil || *g.ShowTaskLinks
}

type GroupDefinition struct {
	Slug         string
	Title        string
	FormLink     string
	Update       bool
	StudentIDs   []string
	Contests     []GroupContestRef
	Grades       *GradesConfig
	MemberGroups []string
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
	// Freeze — переопределение заморозки на уровне группы: nil — как в
	// определении контеста; None — выключить; иначе — своя длительность/all.
	Freeze *FreezeSpec
	// ZeroPenalty — переопределение штрафа за нули: nil — как в определении,
	// 0 — выключить, N — своё значение.
	ZeroPenalty *int
	// SummaryTotalOnly — переопределение «в сводной только сумма»:
	// nil — как в определении.
	SummaryTotalOnly *bool
	// Hidden — переопределение «скрыт на страницах школьников»:
	// nil — как в определении.
	Hidden *bool
}

// FreezeSpec — параметр заморозки: всё соревнование ("all"), длительность от
// конца окна ("1h" → последний час скрыт) или явное выключение ("none" — в
// записи группы отменяет заморозку из определения контеста).
type FreezeSpec struct {
	All      bool
	None     bool
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
	if strings.EqualFold(raw, "none") {
		return &FreezeSpec{None: true}, nil
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
	if f == nil || f.None || start == nil || end == nil || end.Before(*start) {
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
	// FlagReviews — отметки проверки флагов нечестности (ключ — FlagReviewKey):
	// «перенос»/«нарушение» исключают посылки эпизода из подсчёта темпа.
	FlagReviews map[string]FlagReview
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
	// Name — человекочитаемое название задачи для подсказки при наведении на
	// заголовок колонки. Заполняется там, где источник его отдаёт «бесплатно»
	// (Codeforces-контест, сборник informatics, контест ejudge); для одиночных
	// ссылок (отдельная задача, acmp) пусто. Приравнивается к ссылке по
	// видимости: когда ссылки скрыты (до старта или show_task_links=false),
	// имя тоже убирается, чтобы не раскрывать задачу раньше времени.
	Name string `json:"name,omitempty"`
	// Hidden — задача скрыта в источнике (напр. затемнена в сборнике informatics).
	// Сервер вырезает такие колонки из публичного вида, но отдаёт по токену жюри.
	Hidden bool `json:"hidden,omitempty"`
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
	// Accepted[i] == true означает, что задача i решена статусом «зачтено»
	// (ejudge/informatics RUN_ACCEPTED), а не полным OK. Ячейка помечается
	// жёлтой рамкой. Пустой/nil — таких пометок нет.
	Accepted []bool `json:"accepted,omitempty"`
	// Accounts — account_id ученика по сайтам, поддерживающим ссылку на список
	// его посылок по задаче (сейчас только informatics). Нужно фронтенду, чтобы
	// сделать ячейку с посылкой кликабельной. Пусто — таких аккаунтов нет.
	Accounts map[string]string `json:"accounts,omitempty"`
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
	// SummaryTotalOnly — в сводной показывать одной колонкой суммы.
	SummaryTotalOnly bool `json:"summary_total_only,omitempty"`
	// Hidden — контест скрыт на страницах школьников; сервер вырезает его из
	// публичных ответов, но отдаёт при просмотре по токену жюри.
	Hidden bool `json:"hidden,omitempty"`
	// ShortName — краткое название (для колонки «только сумма» в сводной).
	ShortName string `json:"short_name,omitempty"`
	// SourceURL — исходная одиночная ссылка контеста, если он добавлен ровно
	// одной informatics-ссылкой (сборник/глава). По ней в колонке «только сумма»
	// строится ссылка на все посылки ученика по контесту. Пусто — контест из
	// нескольких ссылок или не informatics.
	SourceURL string `json:"source_url,omitempty"`
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
		ID           string                `json:"id"`
		Title        string                `json:"title"`
		ScoreSystem  *ScoreSystem          `json:"score_system"`
		SourceType   *string               `json:"source_type,omitempty"`
		ContestType  *string               `json:"contest_type,omitempty"` // legacy alias
		TableNames   TableNameList         `json:"table_name,omitempty"`
		Materials    []ContestMaterial     `json:"materials,omitempty"`
		GeneratedAt  *time.Time            `json:"generated_at,omitempty"`
		StartTime    *time.Time            `json:"start_time,omitempty"`
		EndTime      *time.Time            `json:"end_time,omitempty"`
		ZeroPenalty  int                   `json:"zero_penalty,omitempty"`
		SummaryTotal bool                  `json:"summary_total_only,omitempty"`
		Hidden       bool                  `json:"hidden,omitempty"`
		ShortName    string                `json:"short_name,omitempty"`
		SourceURL    string                `json:"source_url,omitempty"`
		FrozenAt     *time.Time            `json:"frozen_at,omitempty"`
		Subcontests  []GeneratedSubcontest `json:"subcontests"`
		Tasks        []GeneratedTask       `json:"tasks"`
		Rows         []GeneratedRow        `json:"rows"`
		RowsFull     []GeneratedRow        `json:"rows_full,omitempty"`
	}

	var raw rawGeneratedContest
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = GeneratedContestStandings{
		ID:               raw.ID,
		Title:            raw.Title,
		ContestType:      resolveSourceType(raw.SourceType, raw.ContestType),
		TableNames:       raw.TableNames,
		Materials:        raw.Materials,
		GeneratedAt:      raw.GeneratedAt,
		StartTime:        raw.StartTime,
		EndTime:          raw.EndTime,
		ZeroPenalty:      raw.ZeroPenalty,
		SummaryTotalOnly: raw.SummaryTotal,
		Hidden:           raw.Hidden,
		ShortName:        raw.ShortName,
		SourceURL:        raw.SourceURL,
		FrozenAt:         raw.FrozenAt,
		Subcontests:      raw.Subcontests,
		Tasks:            raw.Tasks,
		Rows:             raw.Rows,
		RowsFull:         raw.RowsFull,
		ScoreSystem:      ScoreSystemEdu,
	}
	if raw.ScoreSystem != nil {
		c.ScoreSystem = raw.ScoreSystem.Normalized()
	}
	return nil
}

type GeneratedGroupStandings struct {
	GroupSlug          string                           `json:"group_slug"`
	GroupTitle         string                           `json:"group_title"`
	GroupShortName     string                           `json:"group_short_name,omitempty"`
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

// StripHiddenContests убирает из публичного ответа контесты с флагом Hidden.
// Оценки уже посчитаны при генерации по всем контестам, поэтому удаление здесь
// на них не влияет — прячется только отображение. Работает на клоне (CloneForServe
// переприсваивает срез Contests), поэтому кэш не задевается.
func (s *GeneratedGroupStandings) StripHiddenContests() {
	if s.Contests == nil {
		return
	}
	kept := make([]GeneratedContestStandings, 0, len(s.Contests))
	for _, c := range s.Contests {
		if !c.Hidden {
			kept = append(kept, c)
		}
	}
	s.Contests = kept
}

// StripHiddenTasks убирает из публичного ответа скрытые задачи-колонки
// (GeneratedTask.Hidden) — их видно только по токену жюри. Для каждого контеста
// вырезаются колонки из Tasks и Subcontests, у каждой строки — соответствующие
// элементы параллельных массивов (Statuses/Scores/…), а счётчики решённых и
// сумма баллов уменьшаются на вклад скрытых задач, чтобы числа сходились с
// видимыми колонками. Оставшиеся задачи перенумеровываются (A, B, C…).
//
// Строки (Rows) в отдаваемой из кэша копии разделяются с оригиналом
// (CloneForServe их не копирует), поэтому здесь для затронутых контестов строки
// и их массивы пересобираются заново, без мутации общих срезов. Место (Place) и
// штраф (Penalty) не пересчитываются: скрытые задачи ученикам недоступны, их
// вклад в ранжирование в обычной ситуации нулевой.
// StripTaskLinks убирает ссылки на задачи из всех контестов (флаг группы
// show_task_links=false для публичного вида): у задач и в заголовках, и в
// плоском списке зануляются URL, поэтому в таблице остаются только метки задач,
// а ячейки с посылками перестают быть кликабельными. Работает на клоне
// (CloneForServe копирует Tasks/Subcontests), кэш не задевается.
func (s *GeneratedGroupStandings) StripTaskLinks() {
	for ci := range s.Contests {
		c := &s.Contests[ci]
		for j := range c.Tasks {
			c.Tasks[j].URL = ""
			c.Tasks[j].NormalizedURL = ""
			c.Tasks[j].Name = ""
		}
		for j := range c.Subcontests {
			for k := range c.Subcontests[j].Tasks {
				c.Subcontests[j].Tasks[k].URL = ""
				c.Subcontests[j].Tasks[k].NormalizedURL = ""
				c.Subcontests[j].Tasks[k].Name = ""
			}
		}
		// SourceURL используется в сводной для ссылки «посылки по контесту» —
		// это тоже ссылка на задачи, убираем.
		c.SourceURL = ""
	}
}

func (s *GeneratedGroupStandings) StripHiddenTasks() {
	for ci := range s.Contests {
		c := &s.Contests[ci]

		hasHidden := false
		for _, t := range c.Tasks {
			if t.Hidden {
				hasHidden = true
				break
			}
		}
		if !hasHidden {
			continue
		}

		// keep[i] — оставить ли глобальную колонку i (в порядке Tasks/строк).
		keep := make([]bool, len(c.Tasks))
		for i, t := range c.Tasks {
			keep[i] = !t.Hidden
		}

		// Пересобираем подконтесты и плоский список Tasks с перенумерацией.
		newTasks := make([]GeneratedTask, 0, len(c.Tasks))
		gi := 0
		for si := range c.Subcontests {
			sub := c.Subcontests[si]
			newSub := make([]GeneratedTask, 0, len(sub.Tasks))
			for _, t := range sub.Tasks {
				if !t.Hidden {
					t.Label = AlphabetLabel(len(newSub))
					newSub = append(newSub, t)
				}
				gi++
			}
			c.Subcontests[si].Tasks = newSub
			c.Subcontests[si].TaskCount = len(newSub)
			newTasks = append(newTasks, newSub...)
		}
		c.Tasks = newTasks

		// Пересобираем строки заново (общий с кэшем срез не мутируем).
		newRows := make([]GeneratedRow, len(c.Rows))
		for ri := range c.Rows {
			newRows[ri] = stripRowHiddenTasks(c.Rows[ri], keep)
		}
		c.Rows = newRows
	}
}

// stripRowHiddenTasks возвращает копию строки без скрытых колонок: параллельные
// массивы фильтруются по keep, а SolvedCount/TotalScore уменьшаются на вклад
// удалённых задач.
func stripRowHiddenTasks(row GeneratedRow, keep []bool) GeneratedRow {
	out := row // копия заголовочных полей; срезы переприсвоим ниже

	pickStr := func(src []string) []string {
		if src == nil {
			return nil
		}
		dst := make([]string, 0, len(src))
		for i, v := range src {
			if i < len(keep) && keep[i] {
				dst = append(dst, v)
			}
		}
		return dst
	}
	pickIntPtr := func(src []*int) []*int {
		if src == nil {
			return nil
		}
		dst := make([]*int, 0, len(src))
		for i, v := range src {
			if i < len(keep) && keep[i] {
				dst = append(dst, v)
			}
		}
		return dst
	}
	pickBool := func(src []bool) []bool {
		if src == nil {
			return nil
		}
		dst := make([]bool, 0, len(src))
		for i, v := range src {
			if i < len(keep) && keep[i] {
				dst = append(dst, v)
			}
		}
		return dst
	}

	// Вычитаем вклад скрытых задач до фильтрации (по исходным индексам).
	for i := range row.Statuses {
		if i < len(keep) && keep[i] {
			continue
		}
		if row.Statuses[i] == TaskStatusSolved && out.SolvedCount > 0 {
			out.SolvedCount--
		}
		if i < len(row.Scores) {
			contribution := 0
			if row.Scores[i] != nil {
				contribution = *row.Scores[i]
			}
			if i < len(row.PracticeScores) && row.PracticeScores[i] != nil && *row.PracticeScores[i] > contribution {
				contribution = *row.PracticeScores[i]
			}
			out.TotalScore -= contribution
		}
	}
	if out.TotalScore < 0 {
		out.TotalScore = 0
	}

	out.Statuses = pickStr(row.Statuses)
	out.Scores = pickIntPtr(row.Scores)
	out.PracticeScores = pickIntPtr(row.PracticeScores)
	out.Upsolved = pickBool(row.Upsolved)
	out.Accepted = pickBool(row.Accepted)
	return out
}

// CloneForServe возвращает копию, которую безопасно отдавать из кэша: серверные
// пер-запросные преобразования представления мутируют её, не задевая исходник.
// Эти преобразования (StripFullRows/SwapInFullRows и скрытие ссылок до старта
// контеста) переприсваивают срез Contests и его элементы, а также правят URL
// внутри Tasks/Subcontests — поэтому копируются заголовок структуры, срез
// Contests и Tasks/Subcontests каждого контеста. Тяжёлые Rows/RowsFull и Grades
// разделяются с исходником: в этих преобразованиях их содержимое не меняется
// (только переприсваиваются заголовки/указатели у копии).
func (s GeneratedGroupStandings) CloneForServe() GeneratedGroupStandings {
	out := s
	if s.Contests == nil {
		return out
	}
	out.Contests = make([]GeneratedContestStandings, len(s.Contests))
	for i, c := range s.Contests {
		cc := c
		if c.Tasks != nil {
			cc.Tasks = append([]GeneratedTask(nil), c.Tasks...)
		}
		if c.Subcontests != nil {
			cc.Subcontests = make([]GeneratedSubcontest, len(c.Subcontests))
			for j, sub := range c.Subcontests {
				ssub := sub
				if sub.Tasks != nil {
					ssub.Tasks = append([]GeneratedTask(nil), sub.Tasks...)
				}
				cc.Subcontests[j] = ssub
			}
		}
		out.Contests[i] = cc
	}
	return out
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
	// Final — итоговое взвешенное среднее, округлённое до Round знаков; nil, если
	// ни одной оценки нет.
	Final *float64 `json:"final,omitempty"`
	// FinalRaw — то же взвешенное среднее без округления (для колонки «точного»
	// итога, которая всегда показывается с двумя знаками). nil — как у Final.
	FinalRaw *float64 `json:"final_raw,omitempty"`
}

type GeneratedGroupSolvedSummaryRow struct {
	StudentID              string `json:"student_id"`
	PublicName             string `json:"public_name"`
	SolvedCountOnPageSites int    `json:"solved_count_on_page_sites"`
	TotalSolvedCount       int    `json:"total_solved_count"`
	SolvedCountBySite      []int  `json:"solved_count_by_site,omitempty"`
}

// GeneratedStudentProfile — профиль участника (админский вид): активность,
// аналитика решений и позиции в группах. Пишется при генерации в
// generated/students/<id>.json. Лента и скорость строятся по посылкам с
// временем (Codeforces, Informatics); ACMP времени не отдаёт — по нему только
// счётчики «решено/попыток».
// StudentCourseStats — темп прохождения курса (страницы группы) учеником:
// сессии по посылкам, эмпирические веса задач, взвешенная скорость. Модель и
// параметры описаны в docs/course_speed.pdf. Видна только преподавателю.
type StudentCourseStats struct {
	GroupSlug  string `json:"group_slug"`
	GroupTitle string `json:"group_title"`
	// Progress — доля пройденного по весу, 0..1.
	Progress float64 `json:"progress"`
	// SolvedCount/TotalCount — решено задач курса / всего задач с весом.
	SolvedCount int `json:"solved_count"`
	TotalCount  int `json:"total_count"`
	// Speed — скорость за всё время (×типичного темпа когорты); ноль — мало данных.
	Speed float64 `json:"speed,omitempty"`
	// SpeedRecent — «текущая форма» с экспоненциальным забыванием (полупериод 28 дней).
	SpeedRecent float64 `json:"speed_recent,omitempty"`
	// ActiveHours — активное время на задачах курса, часов.
	ActiveHours float64 `json:"active_hours"`
	// WeeklyHours — типичная недельная активность (медиана за 8 недель), часов.
	WeeklyHours float64 `json:"weekly_hours,omitempty"`
	// ForecastWeeks — прогноз до конца курса (недель); ноль — не оценить.
	ForecastWeeks float64 `json:"forecast_weeks,omitempty"`
	// Front — самая дальняя решённая задача курса («Контест · A»).
	Front string `json:"front,omitempty"`
	// Stuck — текущие «застревания»: активного времени уже больше z*×типичного.
	Stuck []CourseTaskSignal `json:"stuck,omitempty"`
	// Abandoned — задачи с попытками, брошенные (дальше решено ≥2 задач курса).
	Abandoned []CourseTaskSignal `json:"abandoned,omitempty"`
	// LowData — данных мало (активного времени/решённых меньше порога): скорость
	// не показываем, только прогресс.
	LowData bool `json:"low_data,omitempty"`
	// Flags — эпизоды с признаками нечестности (серия «с первой попытки»,
	// пачка мгновенных решений и т.п.). Сигнал для личной проверки
	// преподавателем, не вердикт.
	Flags []CourseFlag `json:"flags,omitempty"`
}

// CourseFlag — один подозрительный эпизод в посылках ученика.
type CourseFlag struct {
	// Key — стабильный отпечаток эпизода (время начала + первая задача);
	// по нему хранится отметка «проверено» преподавателем.
	Key   string    `json:"key,omitempty"`
	Text  string    `json:"text"`            // краткое описание с числами
	Tasks []string  `json:"tasks,omitempty"` // метки задач эпизода («Контест · A»)
	At    time.Time `json:"at,omitempty"`    // начало эпизода
	// TaskURLs — нормализованные URL всех задач эпизода: по ним посылки эпизода
	// исключаются из подсчёта темпа, если преподаватель отметил «перенос» или
	// «нарушение».
	TaskURLs []string `json:"task_urls,omitempty"`
	// ReviewedAt/ReviewComment/Resolution — отметка проверки (заполняется
	// сервером из data/flag_reviews.json при отдаче страницы, в generated не
	// хранится). Resolution — один из FlagResolution*.
	ReviewedAt    *time.Time `json:"reviewed_at,omitempty"`
	ReviewComment string     `json:"review_comment,omitempty"`
	Resolution    string     `json:"resolution,omitempty"`
}

// Исход проверки флага преподавателем.
const (
	// FlagResolutionLegit — реально сам решил: посылки корректны, всё учитывается.
	FlagResolutionLegit = "legit"
	// FlagResolutionTransfer — перенос посылок (например, с другого сайта):
	// посылки корректны, но эпизод исключается из подсчёта темпа.
	FlagResolutionTransfer = "transfer"
	// FlagResolutionViolation — нарушение: эпизод исключается из темпа, а отметка
	// остаётся подсвеченной в профиле навсегда.
	FlagResolutionViolation = "violation"
)

// FlagResolutionExcludesTempo — исключать ли посылки эпизода из подсчёта темпа.
func FlagResolutionExcludesTempo(resolution string) bool {
	return resolution == FlagResolutionTransfer || resolution == FlagResolutionViolation
}

// FlagReview — отметка преподавателя о проверке флага; хранится в
// data/flag_reviews.json по ключу FlagReviewKey.
type FlagReview struct {
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
	// Resolution — один из FlagResolution*; пусто в старых записях — считается legit.
	Resolution string `json:"resolution,omitempty"`
	// Flag — снапшот флага на момент проверки: по нему эпизод исключается из
	// темпа при генерации и показывается в профиле, когда сам флаг уже не
	// детектируется (посылки исключены после «переноса»/«нарушения»).
	Flag *CourseFlag `json:"flag,omitempty"`
}

// NormalizedResolution — исход с учётом старых записей без поля (legit).
func (r FlagReview) NormalizedResolution() string {
	if r.Resolution == "" {
		return FlagResolutionLegit
	}
	return r.Resolution
}

// FlagReviewKey — ключ отметки в data/flag_reviews.json.
func FlagReviewKey(studentID, groupSlug, flagKey string) string {
	return studentID + "|" + groupSlug + "|" + flagKey
}

// OpenFlags — непроверенные флаги (для счётчика 🚩 в списке участников).
func (s StudentCourseStats) OpenFlags() []CourseFlag {
	out := make([]CourseFlag, 0, len(s.Flags))
	for _, f := range s.Flags {
		if f.ReviewedAt == nil {
			out = append(out, f)
		}
	}
	return out
}

// CourseTaskSignal — сигнальная задача курса для преподавателя.
type CourseTaskSignal struct {
	Label   string  `json:"label"` // «Контест · A»
	Name    string  `json:"name,omitempty"`
	URL     string  `json:"url,omitempty"`
	Ratio   float64 `json:"ratio,omitempty"`   // T_ij / w_j (для застреваний)
	Minutes float64 `json:"minutes,omitempty"` // активное время на задаче
}

type GeneratedStudentProfile struct {
	StudentID     string                 `json:"student_id"`
	PublicName    string                 `json:"public_name"`
	FullName      string                 `json:"full_name,omitempty"`
	Accounts      []Account              `json:"accounts,omitempty"`
	GeneratedAt   *time.Time             `json:"generated_at,omitempty"`
	Groups        []StudentGroupStanding `json:"groups,omitempty"`
	Sites         []StudentSiteStat      `json:"sites,omitempty"`
	Stats         StudentActivityStats   `json:"stats"`
	DailyActivity []StudentDayCount      `json:"daily_activity,omitempty"`
	Recent        []StudentSubmission    `json:"recent,omitempty"`
	// CourseStats — темп по каждому курсу (группе) ученика; для преподавателя.
	CourseStats []StudentCourseStats `json:"course_stats,omitempty"`
}

// StudentSubmission — одна посылка ученика с временем (для ленты).
type StudentSubmission struct {
	At      time.Time `json:"at"`
	Site    string    `json:"site"`
	TaskURL string    `json:"task_url"`
	Label   string    `json:"label"`
	Solved  bool      `json:"solved"`
	Score   *int      `json:"score,omitempty"`
}

// StudentSiteStat — счётчики по сайту. HasTimes=false у сайтов без времени
// посылок (ACMP): по ним нет ленты и скорости.
type StudentSiteStat struct {
	Site        string `json:"site"`
	Solved      int    `json:"solved"`
	Attempted   int    `json:"attempted"`
	Submissions int    `json:"submissions"`
	HasTimes    bool   `json:"has_times"`
}

// StudentActivityStats — агрегаты активности. Метрики скорости считаются только
// по задачам, решённым на сайтах с временем (SolvedWithTimes — знаменатель).
type StudentActivityStats struct {
	TotalSolved        int        `json:"total_solved"`
	TotalAttempted     int        `json:"total_attempted"`
	TotalSubmissions   int        `json:"total_submissions"`
	SolvedToday        int        `json:"solved_today"`
	Submissions7d      int        `json:"submissions_7d"`
	Submissions30d     int        `json:"submissions_30d"`
	ActiveDays         int        `json:"active_days"`
	LastActivity       *time.Time `json:"last_activity,omitempty"`
	SolvedWithTimes    int        `json:"solved_with_times"`
	FirstTrySolved     int        `json:"first_try_solved"`
	AvgAttemptsToSolve float64    `json:"avg_attempts_to_solve,omitempty"`
}

// StudentGroupStanding — позиция ученика в одной группе (по доске почёта и,
// если настроены, по оценкам). Место — стандартный ранг (1 + число тех, кто
// строго выше); ничьи делят место.
type StudentGroupStanding struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	SolvedCount int      `json:"solved_count"`
	HonorPlace  int      `json:"honor_place,omitempty"`
	HonorTotal  int      `json:"honor_total,omitempty"`
	Grade       *float64 `json:"grade,omitempty"`
	GradePlace  int      `json:"grade_place,omitempty"`
	GradeTotal  int      `json:"grade_total,omitempty"`
}

// StudentDayCount — число посылок за один день (для графика активности).
type StudentDayCount struct {
	Date  string `json:"date"` // YYYY-MM-DD (МSK)
	Count int    `json:"count"`
}
