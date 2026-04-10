package standings

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

type accountStatuses struct {
	solved    map[string]struct{}
	attempted map[string]struct{}
	scores    map[string]int
}

type preparedGroup struct {
	group    domain.GroupDefinition
	students []domain.Student
	contests []domain.Contest
}

type Builder struct {
	sources       *source.Registry
	logger        *log.Logger
	maxConcurrent int
}

func NewBuilder(sources *source.Registry, logger *log.Logger, maxConcurrent int) *Builder {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	if logger == nil {
		logger = log.Default()
	}
	if sources == nil {
		sources = source.NewRegistry()
	}

	return &Builder{
		sources:       sources,
		logger:        logger,
		maxConcurrent: maxConcurrent,
	}
}

func (b *Builder) BuildGroupsStandings(ctx context.Context, data *domain.SourceData, groups []domain.GroupDefinition) (map[string]domain.GeneratedGroupStandings, error) {
	if data == nil {
		return nil, fmt.Errorf("source data is nil")
	}

	prepared := b.prepareGroups(data, groups)
	if len(prepared) == 0 {
		return map[string]domain.GeneratedGroupStandings{}, nil
	}

	requiredSites := b.collectRequiredTaskSites(prepared)
	students := uniqueStudents(prepared)
	statusByStudent, err := b.collectStudentsTaskStatuses(ctx, students, requiredSites)
	if err != nil {
		return nil, err
	}

	result := make(map[string]domain.GeneratedGroupStandings, len(prepared))
	for _, pg := range prepared {
		standings, buildErr := b.buildGroupStandings(ctx, data, pg, statusByStudent)
		if buildErr != nil {
			return nil, fmt.Errorf("group=%s build standings: %w", pg.group.Slug, buildErr)
		}
		result[pg.group.Slug] = standings
	}
	return result, nil
}

func (b *Builder) prepareGroups(data *domain.SourceData, groups []domain.GroupDefinition) []preparedGroup {
	out := make([]preparedGroup, 0, len(groups))
	for _, group := range groups {
		students := b.resolveGroupStudents(data, group)
		contests := b.resolveGroupContests(data, group)
		out = append(out, preparedGroup{
			group:    group,
			students: students,
			contests: contests,
		})
	}
	return out
}

func (b *Builder) resolveGroupStudents(data *domain.SourceData, group domain.GroupDefinition) []domain.Student {
	students := make([]domain.Student, 0, len(group.StudentIDs))
	for _, studentID := range group.StudentIDs {
		student, ok := data.Students[studentID]
		if !ok {
			b.logger.Printf("WARN group=%s unknown student_id=%s", group.Slug, studentID)
			continue
		}
		students = append(students, student)
	}
	return students
}

func (b *Builder) resolveGroupContests(data *domain.SourceData, group domain.GroupDefinition) []domain.Contest {
	contests := make([]domain.Contest, 0, len(group.Contests))
	for _, contestRef := range group.Contests {
		contest, ok := data.Contests[contestRef.ID]
		if !ok {
			b.logger.Printf("WARN group=%s unknown contest_id=%s", group.Slug, contestRef.ID)
			continue
		}
		contests = append(contests, contest)
	}
	return contests
}

func uniqueStudents(prepared []preparedGroup) []domain.Student {
	seen := make(map[string]struct{})
	out := make([]domain.Student, 0)
	for _, pg := range prepared {
		for _, student := range pg.students {
			if _, ok := seen[student.ID]; ok {
				continue
			}
			seen[student.ID] = struct{}{}
			out = append(out, student)
		}
	}
	return out
}

func (b *Builder) collectRequiredTaskSites(prepared []preparedGroup) map[string]struct{} {
	out := make(map[string]struct{})
	for _, pg := range prepared {
		for _, contest := range pg.contests {
			if contest.TypeOrDefault() != domain.ContestTypeTasks {
				continue
			}
			for _, subcontest := range contest.Subcontests {
				for _, taskURL := range subcontest.Tasks {
					normalized := domain.NormalizeTaskURL(taskURL)
					site, _, ok := b.sources.ResolveSiteByTaskURL(normalized)
					if !ok {
						continue
					}
					out[domain.NormalizeSite(site)] = struct{}{}
				}
			}
		}
	}
	return out
}

func (b *Builder) collectStudentsTaskStatuses(ctx context.Context, students []domain.Student, requiredSites map[string]struct{}) (map[string]*accountStatuses, error) {
	result := make(map[string]*accountStatuses, len(students))
	for _, student := range students {
		result[student.ID] = newAccountStatuses()
	}

	if len(requiredSites) == 0 || len(students) == 0 {
		return result, nil
	}

	type target struct {
		site      string
		accountID string
	}

	targetByKey := make(map[string]target)
	studentKeys := make(map[string][]string, len(students))
	for _, student := range students {
		seenStudentKeys := make(map[string]struct{})
		for _, account := range student.Accounts {
			site := domain.NormalizeSite(account.Site)
			if _, need := requiredSites[site]; !need {
				continue
			}
			accountID := strings.TrimSpace(account.AccountID)
			if site == "" || accountID == "" {
				continue
			}

			key := site + "|" + accountID
			targetByKey[key] = target{site: site, accountID: accountID}
			if _, exists := seenStudentKeys[key]; exists {
				continue
			}
			seenStudentKeys[key] = struct{}{}
			studentKeys[student.ID] = append(studentKeys[student.ID], key)
		}
	}

	if len(targetByKey) == 0 {
		return result, nil
	}

	statusesByKey := make(map[string]accountStatuses, len(targetByKey))
	statusesMu := sync.Mutex{}
	sem := make(chan struct{}, b.maxConcurrent)
	wg := sync.WaitGroup{}

	for key, t := range targetByKey {
		wg.Add(1)
		go func(key string, t target) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			statuses, err := b.fetchAccountStatuses(ctx, t.site, t.accountID)
			if err != nil {
				b.logger.Printf("WARN site=%s account_id=%s fetch error: %v", t.site, t.accountID, err)
				return
			}

			statusesMu.Lock()
			statusesByKey[key] = statuses
			statusesMu.Unlock()
		}(key, t)
	}

	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for _, student := range students {
		agg := result[student.ID]
		for _, key := range studentKeys[student.ID] {
			statuses, ok := statusesByKey[key]
			if !ok {
				continue
			}
			mergeStatuses(agg, statuses)
		}
	}

	return result, nil
}

func (b *Builder) fetchAccountStatuses(ctx context.Context, site string, accountID string) (accountStatuses, error) {
	client, ok := b.sources.Site(site)
	if !ok {
		b.logger.Printf("WARN unknown site=%s account_id=%s", site, accountID)
		return newAccountStatusesValue(), nil
	}

	results, err := client.FetchUserResults(ctx, accountID)
	if err != nil {
		return accountStatuses{}, err
	}

	out := newAccountStatusesValue()
	for _, result := range results {
		normalized := domain.NormalizeTaskURL(result.TaskURL)
		if normalized == "" {
			continue
		}

		attempted := result.Attempted || result.Solved || result.Score != nil
		if attempted {
			out.attempted[normalized] = struct{}{}
		}
		if result.Solved {
			out.solved[normalized] = struct{}{}
		}

		hasScore := false
		score := 0
		if result.Score != nil {
			score = domain.ClampScore(*result.Score)
			hasScore = true
		} else if attempted {
			if result.Solved {
				score = 100
			}
			hasScore = true
		}

		if hasScore {
			if prev, exists := out.scores[normalized]; !exists || score > prev {
				out.scores[normalized] = score
			}
		}
	}

	return out, nil
}

func (b *Builder) buildGroupStandings(
	ctx context.Context,
	data *domain.SourceData,
	pg preparedGroup,
	statusByStudent map[string]*accountStatuses,
) (domain.GeneratedGroupStandings, error) {
	out := domain.GeneratedGroupStandings{
		GroupSlug:  pg.group.Slug,
		GroupTitle: pg.group.Title,
		FormLink:   pg.group.FormLink,
		Contests:   make([]domain.GeneratedContestStandings, 0, len(pg.contests)),
	}

	for _, contest := range pg.contests {
		switch contest.TypeOrDefault() {
		case domain.ContestTypeTasks:
			out.Contests = append(out.Contests, b.buildTaskContestStandings(contest, pg.students, statusByStudent))
		case domain.ContestTypeProvider:
			generated, err := b.buildProviderContestStandings(ctx, data, pg.group, contest, pg.students)
			if err != nil {
				b.logger.Printf("WARN group=%s contest_id=%s provider build failed; keep previous generated version if available: %v", pg.group.Slug, contest.ID, err)
				continue
			}
			out.Contests = append(out.Contests, generated)
		default:
			return domain.GeneratedGroupStandings{}, fmt.Errorf("contest_id=%s unsupported contest_type=%s", contest.ID, contest.TypeOrDefault())
		}
	}

	return out, nil
}

func (b *Builder) buildProviderContestStandings(
	ctx context.Context,
	data *domain.SourceData,
	group domain.GroupDefinition,
	contest domain.Contest,
	students []domain.Student,
) (domain.GeneratedContestStandings, error) {
	providerID := strings.TrimSpace(contest.Provider)
	if providerID == "" {
		return domain.GeneratedContestStandings{}, fmt.Errorf("provider contest requires non-empty provider")
	}

	provider, ok := b.sources.Provider(providerID)
	if !ok {
		return domain.GeneratedContestStandings{}, fmt.Errorf("unknown provider %q", providerID)
	}

	standings, err := provider.BuildStandings(ctx, source.ContestProviderInput{
		Source:   data,
		Group:    group,
		Contest:  contest,
		Students: students,
	})
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}
	standings.ContestType = domain.ContestTypeProvider
	standings.Materials = domain.NormalizeContestMaterials(contest.Materials)
	return standings, nil
}

func (b *Builder) buildTaskContestStandings(contest domain.Contest, students []domain.Student, statusByStudent map[string]*accountStatuses) domain.GeneratedContestStandings {
	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		Olympiad:    contest.Olympiad,
		ContestType: domain.ContestTypeTasks,
		Materials:   domain.NormalizeContestMaterials(contest.Materials),
		Subcontests: make([]domain.GeneratedSubcontest, 0, len(contest.Subcontests)),
		Tasks:       make([]domain.GeneratedTask, 0),
		Rows:        make([]domain.GeneratedRow, 0, len(students)),
	}

	taskUsesSiteScores := make([]bool, 0)
	for _, subcontest := range contest.Subcontests {
		generatedSubcontest := domain.GeneratedSubcontest{
			Title: subcontest.Title,
			Tasks: make([]domain.GeneratedTask, 0, len(subcontest.Tasks)),
		}
		for i, rawTaskURL := range subcontest.Tasks {
			normalized := domain.NormalizeTaskURL(rawTaskURL)
			task := domain.GeneratedTask{
				Label:         domain.AlphabetLabel(i),
				URL:           strings.TrimSpace(rawTaskURL),
				NormalizedURL: normalized,
			}
			generatedSubcontest.Tasks = append(generatedSubcontest.Tasks, task)
			out.Tasks = append(out.Tasks, task)

			useRealScores := false
			if contest.Olympiad {
				_, client, ok := b.sources.ResolveSiteByTaskURL(normalized)
				if ok && client != nil && client.SupportsTaskScores() {
					useRealScores = true
				}
			}
			taskUsesSiteScores = append(taskUsesSiteScores, useRealScores)
		}
		generatedSubcontest.TaskCount = len(generatedSubcontest.Tasks)
		out.Subcontests = append(out.Subcontests, generatedSubcontest)
	}

	for _, student := range students {
		combined := statusByStudent[student.ID]
		if combined == nil {
			combined = newAccountStatuses()
		}

		row := domain.GeneratedRow{
			StudentID:   student.ID,
			PublicName:  student.PublicName,
			Statuses:    make([]string, len(out.Tasks)),
			SolvedCount: 0,
		}
		if contest.Olympiad {
			row.Scores = make([]*int, len(out.Tasks))
		}

		for i, task := range out.Tasks {
			status := domain.TaskStatusNone
			if _, ok := combined.solved[task.NormalizedURL]; ok {
				status = domain.TaskStatusSolved
				row.SolvedCount++
			} else if _, ok := combined.attempted[task.NormalizedURL]; ok {
				status = domain.TaskStatusAttempted
			}
			row.Statuses[i] = status

			if contest.Olympiad {
				score, ok := resolveTaskScore(status, combined, task.NormalizedURL, taskUsesSiteScores[i])
				if !ok {
					continue
				}
				value := score
				row.Scores[i] = &value
				row.TotalScore += score
			}
		}

		out.Rows = append(out.Rows, row)
	}

	sort.Slice(out.Rows, func(i, j int) bool {
		if contest.Olympiad {
			if out.Rows[i].TotalScore != out.Rows[j].TotalScore {
				return out.Rows[i].TotalScore > out.Rows[j].TotalScore
			}
			if out.Rows[i].SolvedCount != out.Rows[j].SolvedCount {
				return out.Rows[i].SolvedCount > out.Rows[j].SolvedCount
			}
			return strings.ToLower(out.Rows[i].PublicName) < strings.ToLower(out.Rows[j].PublicName)
		}

		if out.Rows[i].SolvedCount != out.Rows[j].SolvedCount {
			return out.Rows[i].SolvedCount > out.Rows[j].SolvedCount
		}
		return strings.ToLower(out.Rows[i].PublicName) < strings.ToLower(out.Rows[j].PublicName)
	})

	return out
}

func resolveTaskScore(status string, combined *accountStatuses, normalizedTaskURL string, useRealScores bool) (int, bool) {
	if status == domain.TaskStatusNone {
		return 0, false
	}

	if useRealScores {
		if score, ok := combined.scores[normalizedTaskURL]; ok {
			return domain.ClampScore(score), true
		}
		if status == domain.TaskStatusSolved {
			return 100, true
		}
		return 0, true
	}

	if status == domain.TaskStatusSolved {
		return 1, true
	}
	return 0, true
}

func newAccountStatuses() *accountStatuses {
	value := newAccountStatusesValue()
	return &value
}

func newAccountStatusesValue() accountStatuses {
	return accountStatuses{
		solved:    make(map[string]struct{}),
		attempted: make(map[string]struct{}),
		scores:    make(map[string]int),
	}
}

func mergeStatuses(dst *accountStatuses, src accountStatuses) {
	if dst == nil {
		return
	}
	for key := range src.solved {
		dst.solved[key] = struct{}{}
	}
	for key := range src.attempted {
		dst.attempted[key] = struct{}{}
	}
	for key, value := range src.scores {
		if prev, ok := dst.scores[key]; !ok || value > prev {
			dst.scores[key] = value
		}
	}
}
