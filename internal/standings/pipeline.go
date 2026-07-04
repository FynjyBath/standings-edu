package standings

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/grades"
	"standings-edu/internal/storage"
)

type Pipeline struct {
	loader          *storage.SourceLoader
	writer          *storage.GeneratedWriter
	generatedLoader *storage.GeneratedLoader
	builder         *Builder
	logger          *log.Logger
}

func NewPipeline(loader *storage.SourceLoader, writer *storage.GeneratedWriter, builder *Builder, logger *log.Logger) *Pipeline {
	if logger == nil {
		logger = log.Default()
	}
	return &Pipeline{
		loader:          loader,
		writer:          writer,
		generatedLoader: storage.NewGeneratedLoader(writer.OutDir),
		builder:         builder,
		logger:          logger,
	}
}

func (p *Pipeline) Run(ctx context.Context, onlyGroup string) error {
	data, err := p.loader.Load()
	if err != nil {
		return fmt.Errorf("load source data: %w", err)
	}

	selectedGroups := selectGroups(data.Groups, onlyGroup)
	if onlyGroup != "" && len(selectedGroups) == 0 {
		return fmt.Errorf("group %q not found", onlyGroup)
	}

	groupsToUpdate := filterGroupsToUpdate(selectedGroups)
	if len(groupsToUpdate) == 0 {
		p.logger.Printf("INFO no groups with update=true selected; nothing to update")
		return nil
	}

	buildGroups := selectGroupsWithUpdatableContests(groupsToUpdate)
	if len(buildGroups) == 0 {
		p.logger.Printf("INFO no contests with update=true in selected groups; nothing to update")
		return nil
	}

	standingsByGroup, err := p.builder.BuildGroupsStandings(ctx, data, buildGroups)
	if err != nil {
		return fmt.Errorf("build standings: %w", err)
	}

	metas := make([]domain.GeneratedGroupMeta, 0, len(selectedGroups))
	for _, group := range selectedGroups {
		metas = append(metas, domain.GeneratedGroupMeta{Slug: group.Slug, Title: group.Title})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	if err := p.writer.WriteGroups(metas); err != nil {
		return fmt.Errorf("write groups list: %w", err)
	}

	fullGroupBySlug := mapGroupsBySlug(groupsToUpdate)

	generatedCount := 0
	for _, group := range buildGroups {
		p.logger.Printf("INFO generating standings for group=%s", group.Slug)

		updatedStandings, ok := standingsByGroup[group.Slug]
		if !ok {
			p.logger.Printf("ERROR group=%s build result not found", group.Slug)
			continue
		}

		fullGroup, ok := fullGroupBySlug[group.Slug]
		if !ok {
			p.logger.Printf("ERROR group=%s source group not found", group.Slug)
			continue
		}
		mergedStandings, ok := p.mergeWithNonUpdatedContests(fullGroup, updatedStandings, data.Students)
		if !ok {
			p.logger.Printf("ERROR group=%s merge failed; skip writing to avoid data loss", group.Slug)
			continue
		}

		// Обновляем отображаемые метаданные (title/table_name/materials) из текущего
		// конфига даже у update=false контестов — их можно менять без пересчёта
		// результатов. Делается до расчёта оценок, чтобы новый table_name учитывался.
		refreshContestMetadata(&mergedStandings, data, fullGroup)

		mergedStandings.Grades = p.buildGroupGrades(fullGroup, mergedStandings, data)

		if err := p.writer.WriteGroupStandings(mergedStandings); err != nil {
			p.logger.Printf("ERROR group=%s write standings failed: %v", group.Slug, err)
			continue
		}

		generatedCount++
		p.logger.Printf("INFO group=%s generated", group.Slug)
	}

	if generatedCount == 0 {
		return fmt.Errorf("no groups generated successfully")
	}

	p.logger.Printf("INFO generation complete: updated %d/%d selected groups", generatedCount, len(buildGroups))
	return nil
}

// refreshContestMetadata накладывает актуальные отображаемые метаданные из конфига
// (title, table_name, materials) на контесты сгенерированных standings. Нужно,
// чтобы смена этих полей подхватывалась даже у update=false контестов, чьи
// результаты переносятся из прошлой генерации без пересчёта.
func refreshContestMetadata(standings *domain.GeneratedGroupStandings, data *domain.SourceData, group domain.GroupDefinition) {
	metaByID := make(map[string]domain.Contest, len(group.Contests))
	for _, contestRef := range group.Contests {
		if contest, ok := resolveGroupContestDef(data, contestRef); ok {
			metaByID[contest.ID] = contest
		}
	}
	for i := range standings.Contests {
		meta, ok := metaByID[standings.Contests[i].ID]
		if !ok {
			continue
		}
		standings.Contests[i].Title = meta.Title
		standings.Contests[i].TableNames = meta.TableNames
		standings.Contests[i].Materials = domain.NormalizeContestMaterials(meta.Materials)
		// Начало контеста (с учётом переопределения из записи группы) — тоже
		// отображаемая метаданная: по ней сервер прячет ссылки до старта.
		standings.Contests[i].StartTime = meta.StartTime
	}
}

// buildGroupGrades считает таблицу оценок группы, если она настроена в group.json.
// При отсутствии конфига или учеников возвращает nil (кнопки оценок не будет).
func (p *Pipeline) buildGroupGrades(group domain.GroupDefinition, standings domain.GeneratedGroupStandings, data *domain.SourceData) *domain.GeneratedGrades {
	if group.Grades == nil || len(group.Grades.Columns) == 0 {
		return nil
	}

	manual, err := p.loader.LoadManualGrades(group.Slug)
	if err != nil {
		p.logger.Printf("WARN group=%s load manual grades failed: %v", group.Slug, err)
		manual = nil
	}

	roster := make([]grades.RosterStudent, 0, len(group.StudentIDs))
	for _, studentID := range group.StudentIDs {
		publicName := studentID
		if student, ok := data.Students[studentID]; ok {
			if name := student.PublicName; name != "" {
				publicName = name
			}
		}
		roster = append(roster, grades.RosterStudent{ID: studentID, PublicName: publicName})
	}

	return grades.Build(group.Grades, standings, roster, manual)
}

func (p *Pipeline) mergeWithNonUpdatedContests(group domain.GroupDefinition, updated domain.GeneratedGroupStandings, students map[string]domain.Student) (domain.GeneratedGroupStandings, bool) {
	hasNonUpdatedContests := false
	for _, contest := range group.Contests {
		if !contest.Update {
			hasNonUpdatedContests = true
			break
		}
	}

	existing := domain.GeneratedGroupStandings{}
	hasExisting := false
	existingLoaded, loadErr := p.generatedLoader.LoadGroupStandings(group.Slug)
	if loadErr == nil {
		existing = existingLoaded
		hasExisting = true
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		p.logger.Printf("WARN group=%s load existing standings failed: %v", group.Slug, loadErr)
	}

	if hasNonUpdatedContests && !hasExisting {
		if errors.Is(loadErr, os.ErrNotExist) {
			p.logger.Printf("WARN group=%s has contests with update=false but previous standings are missing", group.Slug)
		}
		return domain.GeneratedGroupStandings{}, false
	}

	updatedByID, err := mapContestsByID(updated.Contests)
	if err != nil {
		p.logger.Printf("WARN group=%s updated standings have duplicate contest ids: %v", group.Slug, err)
		return domain.GeneratedGroupStandings{}, false
	}
	existingByID := map[string]domain.GeneratedContestStandings{}
	if hasExisting {
		existingByID, err = mapContestsByID(existing.Contests)
		if err != nil {
			p.logger.Printf("WARN group=%s existing standings have duplicate contest ids: %v", group.Slug, err)
			return domain.GeneratedGroupStandings{}, false
		}
	}

	// Доска почёта всегда свежая: builder считает её по всем контестам группы
	// (включая update=false), поэтому новые участники появляются сразу.
	merged := domain.GeneratedGroupStandings{
		GroupSlug:          group.Slug,
		GroupTitle:         group.Title,
		FormLink:           group.FormLink,
		SolvedSummarySites: updated.SolvedSummarySites,
		SolvedSummary:      updated.SolvedSummary,
		Contests:           make([]domain.GeneratedContestStandings, 0, len(group.Contests)),
	}

	for _, contestRef := range group.Contests {
		if contestRef.Update {
			if contest, ok := updatedByID[contestRef.ID]; ok {
				merged.Contests = append(merged.Contests, contest)
				continue
			}
			if hasExisting {
				if oldContest, oldOK := existingByID[contestRef.ID]; oldOK {
					p.logger.Printf("WARN group=%s contest=%s update=true but not built; keep previous generated version", group.Slug, contestRef.ID)
					merged.Contests = append(merged.Contests, oldContest)
					continue
				}
			}
			p.logger.Printf("WARN group=%s contest=%s update=true but not built and no previous version found", group.Slug, contestRef.ID)
			continue
		}

		contest, ok := existingByID[contestRef.ID]
		if !ok {
			p.logger.Printf("WARN group=%s contest=%s update=false but missing in previous standings", group.Slug, contestRef.ID)
			continue
		}
		reconcileContestRoster(&contest, group, students)
		merged.Contests = append(merged.Contests, contest)
	}

	return merged, true
}

// reconcileContestRoster согласует строки перенесённого без пересчёта контеста
// (update=false) с текущим составом группы: новые участники получают пустые
// строки, выбывшие убираются, имена обновляются. Результаты не трогаем.
// Легаси-таблицы, где у строк нет student_id, оставляем как есть — сопоставить
// их с учениками не по чему.
func reconcileContestRoster(contest *domain.GeneratedContestStandings, group domain.GroupDefinition, students map[string]domain.Student) {
	rowByStudent := make(map[string]domain.GeneratedRow, len(contest.Rows))
	for _, row := range contest.Rows {
		if strings.TrimSpace(row.StudentID) == "" {
			return // легаси-формат без student_id
		}
		rowByStudent[row.StudentID] = row
	}

	roster := make(map[string]struct{}, len(group.StudentIDs))
	for _, studentID := range group.StudentIDs {
		roster[studentID] = struct{}{}
	}

	// Существующие строки — в прежнем порядке (соответствует местам), без выбывших.
	rows := make([]domain.GeneratedRow, 0, len(group.StudentIDs))
	for _, row := range contest.Rows {
		if _, inRoster := roster[row.StudentID]; !inRoster {
			continue
		}
		if student, ok := students[row.StudentID]; ok && strings.TrimSpace(student.PublicName) != "" {
			row.PublicName = student.PublicName
		}
		rows = append(rows, row)
	}

	// Новые участники — пустые строки в конце, в порядке состава группы.
	for _, studentID := range group.StudentIDs {
		if _, has := rowByStudent[studentID]; has {
			continue
		}
		publicName := studentID
		if student, ok := students[studentID]; ok && strings.TrimSpace(student.PublicName) != "" {
			publicName = student.PublicName
		}
		statuses := make([]string, len(contest.Tasks))
		for i := range statuses {
			statuses[i] = domain.TaskStatusNone
		}
		rows = append(rows, domain.GeneratedRow{
			StudentID:  studentID,
			PublicName: publicName,
			Statuses:   statuses,
		})
	}

	contest.Rows = rows
}

func mapContestsByID(contests []domain.GeneratedContestStandings) (map[string]domain.GeneratedContestStandings, error) {
	out := make(map[string]domain.GeneratedContestStandings, len(contests))
	for _, contest := range contests {
		if _, exists := out[contest.ID]; exists {
			return nil, fmt.Errorf("duplicate contest id %q", contest.ID)
		}
		out[contest.ID] = contest
	}
	return out, nil
}

func mapGroupsBySlug(groups []domain.GroupDefinition) map[string]domain.GroupDefinition {
	out := make(map[string]domain.GroupDefinition, len(groups))
	for _, group := range groups {
		out[group.Slug] = group
	}
	return out
}

func filterGroupsToUpdate(groups []domain.GroupDefinition) []domain.GroupDefinition {
	out := make([]domain.GroupDefinition, 0, len(groups))
	for _, group := range groups {
		if group.Update {
			out = append(out, group)
		}
	}
	return out
}

// selectGroupsWithUpdatableContests оставляет группы, где есть хоть один контест
// (builder сам пересобирает только update=true, но доску почёта, оценки и состав
// участников обновляет по всей группе — поэтому контесты здесь не фильтруются).
func selectGroupsWithUpdatableContests(groups []domain.GroupDefinition) []domain.GroupDefinition {
	out := make([]domain.GroupDefinition, 0, len(groups))
	for _, group := range groups {
		if len(group.Contests) == 0 {
			continue
		}
		out = append(out, group)
	}
	return out
}

func selectGroups(all []domain.GroupDefinition, onlyGroup string) []domain.GroupDefinition {
	if onlyGroup == "" {
		return all
	}
	out := make([]domain.GroupDefinition, 0, 1)
	for _, g := range all {
		if g.Slug == onlyGroup {
			out = append(out, g)
			break
		}
	}
	return out
}
