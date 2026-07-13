package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/source"
)

type SourceLoader struct {
	DataDir string
}

func NewSourceLoader(dataDir string) *SourceLoader {
	return &SourceLoader{DataDir: dataDir}
}

func (l *SourceLoader) Load() (*domain.SourceData, error) {
	students, err := l.loadStudents()
	if err != nil {
		return nil, err
	}
	contests, err := l.loadContests()
	if err != nil {
		return nil, err
	}
	groups, err := l.loadGroups()
	if err != nil {
		return nil, err
	}

	return &domain.SourceData{
		Students: students,
		Contests: contests,
		Groups:   groups,
	}, nil
}

func (l *SourceLoader) loadStudents() (map[string]domain.Student, error) {
	path := filepath.Join(l.DataDir, "students.json")
	var students []domain.Student
	if err := fileutil.ReadJSON(path, &students); err != nil {
		return nil, fmt.Errorf("load students: %w", err)
	}

	out := make(map[string]domain.Student, len(students))
	for i, s := range students {
		s = domain.NormalizeStudent(s)
		if s.ID == "" {
			continue
		}
		if s.FullName == "" {
			return nil, fmt.Errorf("student item #%d has empty full_name", i)
		}
		if s.PublicName == "" {
			s.PublicName = s.FullName
		}
		out[s.ID] = s
	}
	return out, nil
}

func (l *SourceLoader) loadContests() (map[string]domain.Contest, error) {
	path := filepath.Join(l.DataDir, "contests.json")
	var contests []domain.Contest
	if err := fileutil.ReadJSON(path, &contests); err != nil {
		return nil, fmt.Errorf("load contests: %w", err)
	}
	tables := l.loadManualTables(filepath.Join(l.DataDir, source.ManualTablesFileName))

	out := make(map[string]domain.Contest, len(contests))
	for _, c := range contests {
		c.ID = strings.TrimSpace(c.ID)
		if c.ID == "" {
			continue
		}
		c.Title = strings.TrimSpace(c.Title)
		c.ScoreSystem = c.ScoreSystem.Normalized()
		c.Provider = strings.TrimSpace(c.Provider)
		c.Materials = domain.NormalizeContestMaterials(c.Materials)
		if _, err := domain.ParseFreezeSpec(c.Freeze); err != nil {
			return nil, fmt.Errorf("contest %q: %w", c.ID, err)
		}
		if err := injectManualTable(&c, c.ID, tables); err != nil {
			return nil, fmt.Errorf("contest %q: %w", c.ID, err)
		}
		out[c.ID] = c
	}
	return out, nil
}

// loadManualTables читает файл таблиц кондуитов (map contest_id -> TSV).
// Нет файла — пустая карта (оценки лежат в provider_config, старый формат).
func (l *SourceLoader) loadManualTables(path string) map[string]string {
	tables := map[string]string{}
	if err := fileutil.ReadJSON(path, &tables); err != nil {
		return map[string]string{}
	}
	return tables
}

// injectManualTable подставляет таблицу оценок из manual_tables.json в
// provider_config кондуита (по id записи). Записи в файле имеют приоритет над
// таблицей, оставшейся в конфиге (легаси-формат).
func injectManualTable(c *domain.Contest, id string, tables map[string]string) error {
	if c.Provider != source.ManualTableProviderID {
		return nil
	}
	table, ok := tables[id]
	if !ok {
		return nil
	}
	cfg, err := source.InjectManualTable(c.ProviderConfig, table)
	if err != nil {
		return err
	}
	c.ProviderConfig = cfg
	return nil
}

func (l *SourceLoader) loadGroups() ([]domain.GroupDefinition, error) {
	groupsDir := filepath.Join(l.DataDir, "groups")
	entries, err := os.ReadDir(groupsDir)
	if err != nil {
		return nil, fmt.Errorf("read groups dir %q: %w", groupsDir, err)
	}

	groups := make([]domain.GroupDefinition, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		if !domain.IsValidSlug(slug) {
			continue
		}
		dir := filepath.Join(groupsDir, slug)

		var gf domain.GroupFile
		if err := fileutil.ReadJSON(filepath.Join(dir, "group.json"), &gf); err != nil {
			return nil, fmt.Errorf("load group %q: %w", slug, err)
		}

		contests, err := l.loadGroupContests(filepath.Join(dir, "contests.json"))
		if err != nil {
			return nil, fmt.Errorf("load group contests %q: %w", slug, err)
		}
		// Таблицы inline-кондуитов группы лежат рядом, в manual_tables.json.
		groupTables := l.loadManualTables(filepath.Join(dir, source.ManualTablesFileName))
		for i := range contests {
			if contests[i].Inline == nil {
				continue
			}
			if err := injectManualTable(contests[i].Inline, contests[i].ID, groupTables); err != nil {
				return nil, fmt.Errorf("group %q contest %q: %w", slug, contests[i].ID, err)
			}
		}

		update := true
		if gf.Update != nil {
			update = *gf.Update
		}

		title := strings.TrimSpace(gf.Title)
		if title == "" {
			title = slug
		}

		groups = append(groups, domain.GroupDefinition{
			Slug:         slug,
			Title:        title,
			FormLink:     strings.TrimSpace(gf.FormLink),
			Update:       update,
			StudentIDs:   domain.NormalizeGroups(gf.StudentIDs),
			Contests:     contests,
			Grades:       gf.Grades,
			MemberGroups: normalizeMemberGroups(gf.MemberGroups, slug),
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Slug < groups[j].Slug
	})

	return groups, nil
}

// normalizeMemberGroups чистит список слагов объединённой группы: пробелы, пустые,
// дубликаты и ссылку на саму себя убираются, порядок сохраняется.
func normalizeMemberGroups(raw []string, self string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, slug := range raw {
		slug = strings.TrimSpace(slug)
		if slug == "" || slug == self || !domain.IsValidSlug(slug) {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LoadManualGrades читает ручные оценки группы из
// data/groups/<slug>/grades_manual.json (columnID -> studentID -> оценка).
// Файла нет — пустая карта, без ошибки.
func (l *SourceLoader) LoadManualGrades(slug string) (map[string]map[string]float64, error) {
	path := filepath.Join(l.DataDir, "groups", slug, "grades_manual.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]map[string]float64{}, nil
		}
		return nil, fmt.Errorf("read manual grades %q: %w", path, err)
	}

	var raw map[string]map[string]float64
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode manual grades %q: %w", path, err)
	}
	if raw == nil {
		raw = map[string]map[string]float64{}
	}
	return raw, nil
}

// inlineContestKeys — ключи, наличие любого из которых означает, что элемент в
// файле группы является не ссылкой по id, а полным определением контеста.
// table_name намеренно НЕ входит в этот список: это метаданные о размещении
// (в какие вкладки попадает контест), а не описание контеста. Ссылка на
// глобальный контест с одним лишь table_name остаётся ссылкой (с переопределением
// вкладок), а не превращается в пустой inline-контест.
var inlineContestKeys = []string{
	"title", "score_system", "source_type", "contest_type", "provider", "provider_config", "subcontests", "materials",
}

func (l *SourceLoader) loadGroupContests(path string) ([]domain.GroupContestRef, error) {
	var items []json.RawMessage
	if err := fileutil.ReadJSON(path, &items); err != nil {
		// Нет файла — у группы просто нет своих контестов (объединённая группа,
		// или свежесозданная). Это не ошибка.
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]domain.GroupContestRef, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i, raw := range items {
		ref, err := parseGroupContestItem(raw)
		if err != nil {
			return nil, fmt.Errorf("contest item #%d in %q: %w", i, path, err)
		}
		if ref.ID == "" {
			return nil, fmt.Errorf("contest item #%d in %q has empty id", i, path)
		}
		if _, exists := seen[ref.ID]; exists {
			return nil, fmt.Errorf("contest item #%d in %q duplicates id %q", i, path, ref.ID)
		}
		seen[ref.ID] = struct{}{}

		out = append(out, ref)
	}

	return out, nil
}

func parseGroupContestItem(raw json.RawMessage) (domain.GroupContestRef, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return domain.GroupContestRef{}, err
	}

	var meta struct {
		ID               string               `json:"id"`
		Update           *bool                `json:"update"`
		TableNames       domain.TableNameList `json:"table_name"`
		StartTime        *time.Time           `json:"start_time"`
		EndTime          *time.Time           `json:"end_time"`
		Freeze           string               `json:"freeze"`
		ZeroPenalty      *int                 `json:"zero_penalty"`
		SummaryTotalOnly *bool                `json:"summary_total_only"`
		Hidden           *bool                `json:"hidden"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return domain.GroupContestRef{}, err
	}

	freeze, err := domain.ParseFreezeSpec(meta.Freeze)
	if err != nil {
		return domain.GroupContestRef{}, err
	}
	if meta.ZeroPenalty != nil && *meta.ZeroPenalty < 0 {
		return domain.GroupContestRef{}, fmt.Errorf("zero_penalty: ожидается неотрицательное число")
	}

	ref := domain.GroupContestRef{
		ID:               strings.TrimSpace(meta.ID),
		Update:           true,
		TableNames:       meta.TableNames,
		StartTime:        meta.StartTime,
		EndTime:          meta.EndTime,
		Freeze:           freeze,
		ZeroPenalty:      meta.ZeroPenalty,
		SummaryTotalOnly: meta.SummaryTotalOnly,
		Hidden:           meta.Hidden,
	}
	if meta.Update != nil {
		ref.Update = *meta.Update
	}

	if !hasInlineContestKeys(keys) {
		return ref, nil
	}

	var contest domain.Contest
	if err := json.Unmarshal(raw, &contest); err != nil {
		return domain.GroupContestRef{}, fmt.Errorf("decode inline contest: %w", err)
	}
	contest.ID = strings.TrimSpace(contest.ID)
	contest.Title = strings.TrimSpace(contest.Title)
	contest.ScoreSystem = contest.ScoreSystem.Normalized()
	contest.Provider = strings.TrimSpace(contest.Provider)
	contest.Materials = domain.NormalizeContestMaterials(contest.Materials)
	ref.Inline = &contest

	return ref, nil
}

func hasInlineContestKeys(keys map[string]json.RawMessage) bool {
	for _, key := range inlineContestKeys {
		if _, ok := keys[key]; ok {
			return true
		}
	}
	return false
}
