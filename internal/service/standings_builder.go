package service

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/cache"
	"standings-edu/internal/domain"
	"standings-edu/internal/sites"
)

type accountStatuses struct {
	solved    map[string]struct{}
	attempted map[string]struct{}
	scores    map[string]int
}

type inflightCall struct {
	done chan struct{}
	res  accountStatuses
	err  error
}

type accountFetchResult struct {
	studentID string
	site      string
	statuses  accountStatuses
}

type StandingsBuilder struct {
	registry      *sites.Registry
	logger        *log.Logger
	maxConcurrent int
	cache         *cache.TTLCache[accountStatuses]

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall
}

func NewStandingsBuilder(registry *sites.Registry, logger *log.Logger, maxConcurrent int, cacheTTL time.Duration) *StandingsBuilder {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	if cacheTTL <= 0 {
		cacheTTL = 5 * time.Minute
	}
	if logger == nil {
		logger = log.Default()
	}

	return &StandingsBuilder{
		registry:      registry,
		logger:        logger,
		maxConcurrent: maxConcurrent,
		cache:         cache.NewTTLCache[accountStatuses](cacheTTL),
		inflight:      make(map[string]*inflightCall),
	}
}

func (b *StandingsBuilder) BuildGroupStandings(ctx context.Context, source *domain.SourceData, group domain.GroupDefinition) (domain.GeneratedGroupStandings, error) {
	if source == nil {
		return domain.GeneratedGroupStandings{}, fmt.Errorf("source data is nil")
	}

	students := b.resolveGroupStudents(source, group)
	contests := b.resolveGroupContests(source, group)
	statusByStudent, err := b.collectStudentStatuses(ctx, students)
	if err != nil {
		return domain.GeneratedGroupStandings{}, err
	}

	return b.buildGroupStandingsPrepared(group, contests, students, statusByStudent), nil
}

func (b *StandingsBuilder) BuildOverallStandings(ctx context.Context, source *domain.SourceData, groups []domain.GroupDefinition) (domain.GeneratedOverallStandings, error) {
	if source == nil {
		return domain.GeneratedOverallStandings{}, fmt.Errorf("source data is nil")
	}

	students := b.resolveStudentsForGroups(source, groups)
	sitesList := b.collectSites()
	statusByStudentSite, err := b.collectStudentStatusesBySite(ctx, students)
	if err != nil {
		return domain.GeneratedOverallStandings{}, err
	}

	return b.buildOverallStandingsPrepared(students, sitesList, statusByStudentSite), nil
}

func (b *StandingsBuilder) BuildAllStandings(ctx context.Context, source *domain.SourceData, groups []domain.GroupDefinition) (domain.GeneratedOverallStandings, map[string]domain.GeneratedGroupStandings, error) {
	if source == nil {
		return domain.GeneratedOverallStandings{}, nil, fmt.Errorf("source data is nil")
	}

	allStudents := b.resolveStudentsForGroups(source, groups)
	sitesList := b.collectSites()

	statusByStudentSite, err := b.collectStudentStatusesBySite(ctx, allStudents)
	if err != nil {
		return domain.GeneratedOverallStandings{}, nil, err
	}

	combinedByStudent := b.buildCombinedStatusesByStudent(allStudents, statusByStudentSite)
	overall := b.buildOverallStandingsPrepared(allStudents, sitesList, statusByStudentSite)

	groupStandings := make(map[string]domain.GeneratedGroupStandings, len(groups))
	for _, group := range groups {
		groupStudents := b.resolveGroupStudents(source, group)
		contests := b.resolveGroupContests(source, group)
		statusByStudent := b.pickCombinedStatuses(groupStudents, combinedByStudent)
		groupStandings[group.Slug] = b.buildGroupStandingsPrepared(group, contests, groupStudents, statusByStudent)
	}

	return overall, groupStandings, nil
}

func (b *StandingsBuilder) resolveGroupStudents(source *domain.SourceData, group domain.GroupDefinition) []domain.Student {
	students := make([]domain.Student, 0, len(group.StudentIDs))
	for _, studentID := range group.StudentIDs {
		student, ok := source.Students[studentID]
		if !ok {
			b.logger.Printf("WARN group=%s unknown student_id=%s", group.Slug, studentID)
			continue
		}
		students = append(students, student)
	}
	return students
}

func (b *StandingsBuilder) resolveStudentsForGroups(source *domain.SourceData, groups []domain.GroupDefinition) []domain.Student {
	seen := make(map[string]struct{})
	students := make([]domain.Student, 0)

	for _, group := range groups {
		for _, studentID := range group.StudentIDs {
			if _, ok := seen[studentID]; ok {
				continue
			}
			student, ok := source.Students[studentID]
			if !ok {
				b.logger.Printf("WARN group=%s unknown student_id=%s", group.Slug, studentID)
				continue
			}
			students = append(students, student)
			seen[studentID] = struct{}{}
		}
	}

	return students
}

func (b *StandingsBuilder) resolveGroupContests(source *domain.SourceData, group domain.GroupDefinition) []domain.Contest {
	contests := make([]domain.Contest, 0, len(group.ContestIDs))
	for _, contestID := range group.ContestIDs {
		contest, ok := source.Contests[contestID]
		if !ok {
			b.logger.Printf("WARN group=%s unknown contest_id=%s", group.Slug, contestID)
			continue
		}
		contests = append(contests, contest)
	}
	return contests
}

func (b *StandingsBuilder) collectSites() []string {
	sitesSet := make(map[string]struct{})
	registered := b.registry.Sites()
	for _, site := range registered {
		sitesSet[site] = struct{}{}
	}

	sitesList := make([]string, 0, len(sitesSet))
	for site := range sitesSet {
		sitesList = append(sitesList, site)
	}
	sort.Strings(sitesList)
	return sitesList
}

func (b *StandingsBuilder) buildGroupStandingsPrepared(group domain.GroupDefinition, contests []domain.Contest, students []domain.Student, statusByStudent map[string]*accountStatuses) domain.GeneratedGroupStandings {
	out := domain.GeneratedGroupStandings{
		GroupSlug:  group.Slug,
		GroupTitle: group.Title,
		Contests:   make([]domain.GeneratedContestStandings, 0, len(contests)),
	}

	for _, contest := range contests {
		generatedContest := b.buildContestStandings(contest, students, statusByStudent)
		out.Contests = append(out.Contests, generatedContest)
	}

	return out
}

func (b *StandingsBuilder) buildOverallStandingsPrepared(students []domain.Student, sitesList []string, statusByStudentSite map[string]map[string]*accountStatuses) domain.GeneratedOverallStandings {
	rows := make([]domain.GeneratedOverallRow, 0, len(students))
	for _, student := range students {
		perSite := make([]int, len(sitesList))
		totalSolved := 0

		studentStatuses := statusByStudentSite[student.ID]
		for i, site := range sitesList {
			siteStatuses := studentStatuses[site]
			if siteStatuses == nil {
				continue
			}
			solvedCount := len(siteStatuses.solved)
			perSite[i] = solvedCount
			totalSolved += solvedCount
		}

		rows = append(rows, domain.GeneratedOverallRow{
			StudentID:    student.ID,
			FullName:     student.FullName,
			SolvedBySite: perSite,
			TotalSolved:  totalSolved,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalSolved != rows[j].TotalSolved {
			return rows[i].TotalSolved > rows[j].TotalSolved
		}
		return strings.ToLower(rows[i].FullName) < strings.ToLower(rows[j].FullName)
	})

	return domain.GeneratedOverallStandings{
		Sites: sitesList,
		Rows:  rows,
	}
}

func (b *StandingsBuilder) buildContestStandings(contest domain.Contest, students []domain.Student, statusByStudent map[string]*accountStatuses) domain.GeneratedContestStandings {
	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		Olympiad:    contest.Olympiad,
		Subcontests: make([]domain.GeneratedSubcontest, 0, len(contest.Subcontests)),
		Tasks:       make([]domain.GeneratedTask, 0),
		Rows:        make([]domain.GeneratedRow, 0, len(students)),
	}

	taskUsesSiteScores := make([]bool, 0)
	for _, sc := range contest.Subcontests {
		genSub := domain.GeneratedSubcontest{
			Title: sc.Title,
			Tasks: make([]domain.GeneratedTask, 0, len(sc.Tasks)),
		}
		for i, taskURL := range sc.Tasks {
			normalized := domain.NormalizeTaskURL(taskURL)
			task := domain.GeneratedTask{
				Label:         alphabetLabel(i),
				URL:           strings.TrimSpace(taskURL),
				NormalizedURL: normalized,
			}
			genSub.Tasks = append(genSub.Tasks, task)
			out.Tasks = append(out.Tasks, task)

			useRealScores := false
			if contest.Olympiad {
				_, client, ok := b.registry.ResolveByTaskURL(normalized)
				if ok && client != nil && client.SupportsTaskScores() {
					useRealScores = true
				}
			}
			taskUsesSiteScores = append(taskUsesSiteScores, useRealScores)
		}
		genSub.TaskCount = len(genSub.Tasks)
		out.Subcontests = append(out.Subcontests, genSub)
	}

	for _, student := range students {
		combined := statusByStudent[student.ID]
		if combined == nil {
			combined = newAccountStatuses()
		}

		row := domain.GeneratedRow{
			StudentID:   student.ID,
			FullName:    student.FullName,
			SolvedCount: 0,
			Statuses:    make([]string, len(out.Tasks)),
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
				if ok {
					value := score
					row.Scores[i] = &value
					row.TotalScore += score
				}
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
			return strings.ToLower(out.Rows[i].FullName) < strings.ToLower(out.Rows[j].FullName)
		}

		if out.Rows[i].SolvedCount != out.Rows[j].SolvedCount {
			return out.Rows[i].SolvedCount > out.Rows[j].SolvedCount
		}
		return strings.ToLower(out.Rows[i].FullName) < strings.ToLower(out.Rows[j].FullName)
	})

	return out
}

func resolveTaskScore(status string, combined *accountStatuses, normalizedTaskURL string, useRealScores bool) (int, bool) {
	if status == domain.TaskStatusNone {
		return 0, false
	}

	if useRealScores {
		if score, ok := combined.scores[normalizedTaskURL]; ok {
			return clampScore(score, 0, 100), true
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

func clampScore(v int, min int, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (b *StandingsBuilder) collectStudentStatuses(ctx context.Context, students []domain.Student) (map[string]*accountStatuses, error) {
	statusByStudentSite, err := b.collectStudentStatusesBySite(ctx, students)
	if err != nil {
		return nil, err
	}
	return b.buildCombinedStatusesByStudent(students, statusByStudentSite), nil
}

func (b *StandingsBuilder) buildCombinedStatusesByStudent(students []domain.Student, statusByStudentSite map[string]map[string]*accountStatuses) map[string]*accountStatuses {
	result := make(map[string]*accountStatuses, len(students))
	for _, student := range students {
		agg := newAccountStatuses()
		for _, statuses := range statusByStudentSite[student.ID] {
			if statuses == nil {
				continue
			}
			mergeStatuses(agg, *statuses)
		}
		result[student.ID] = agg
	}
	return result
}

func (b *StandingsBuilder) pickCombinedStatuses(students []domain.Student, combinedByStudent map[string]*accountStatuses) map[string]*accountStatuses {
	result := make(map[string]*accountStatuses, len(students))
	for _, student := range students {
		if statuses, ok := combinedByStudent[student.ID]; ok && statuses != nil {
			result[student.ID] = statuses
			continue
		}
		result[student.ID] = newAccountStatuses()
	}
	return result
}

func (b *StandingsBuilder) collectStudentStatusesBySite(ctx context.Context, students []domain.Student) (map[string]map[string]*accountStatuses, error) {
	result := make(map[string]map[string]*accountStatuses, len(students))
	for _, student := range students {
		result[student.ID] = make(map[string]*accountStatuses)
	}

	resultsCh := make(chan accountFetchResult)
	sem := make(chan struct{}, b.maxConcurrent)
	wg := sync.WaitGroup{}

	for _, student := range students {
		for _, account := range student.Accounts {
			account := account
			studentID := student.ID
			site := normalizeSite(account.Site)
			accountID := strings.TrimSpace(account.AccountID)
			if site == "" || accountID == "" {
				continue
			}

			wg.Add(1)
			go func() {
				defer wg.Done()

				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()

				statuses, err := b.fetchAccountStatuses(ctx, site, accountID)
				if err != nil {
					b.logger.Printf("WARN student_id=%s site=%s account_id=%s fetch error: %v", studentID, site, accountID, err)
					return
				}

				select {
				case resultsCh <- accountFetchResult{studentID: studentID, site: site, statuses: statuses}:
				case <-ctx.Done():
				}
			}()
		}
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	for res := range resultsCh {
		studentSites := result[res.studentID]
		agg := studentSites[res.site]
		if agg == nil {
			agg = newAccountStatuses()
			studentSites[res.site] = agg
		}
		mergeStatuses(agg, res.statuses)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (b *StandingsBuilder) fetchAccountStatuses(ctx context.Context, site string, accountID string) (accountStatuses, error) {
	site = normalizeSite(site)
	accountID = strings.TrimSpace(accountID)
	cacheKey := site + ":" + accountID

	if cached, ok := b.cache.Get(cacheKey); ok {
		return cloneStatuses(cached), nil
	}

	call := b.acquireInflight(cacheKey)
	if call != nil {
		select {
		case <-call.done:
			return cloneStatuses(call.res), call.err
		case <-ctx.Done():
			return accountStatuses{}, ctx.Err()
		}
	}

	res, err := b.loadFromSite(ctx, site, accountID)
	if err == nil {
		b.cache.Set(cacheKey, res)
	}
	b.resolveInflight(cacheKey, res, err)
	return cloneStatuses(res), err
}

func (b *StandingsBuilder) loadFromSite(ctx context.Context, site string, accountID string) (accountStatuses, error) {
	client, ok := b.registry.Get(site)
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
			score = clampScore(*result.Score, 0, 100)
			hasScore = true
		} else if attempted {
			if result.Solved {
				score = 100
			} else {
				score = 0
			}
			hasScore = true
		}

		if hasScore {
			if prev, ok := out.scores[normalized]; !ok || score > prev {
				out.scores[normalized] = score
			}
		}
	}

	return out, nil
}

func (b *StandingsBuilder) acquireInflight(cacheKey string) *inflightCall {
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()

	if call, ok := b.inflight[cacheKey]; ok {
		return call
	}

	b.inflight[cacheKey] = &inflightCall{done: make(chan struct{})}
	return nil
}

func (b *StandingsBuilder) resolveInflight(cacheKey string, res accountStatuses, err error) {
	b.inflightMu.Lock()
	call := b.inflight[cacheKey]
	delete(b.inflight, cacheKey)
	b.inflightMu.Unlock()

	if call == nil {
		return
	}
	call.res = res
	call.err = err
	close(call.done)
}

func newAccountStatuses() *accountStatuses {
	statuses := newAccountStatusesValue()
	return &statuses
}

func newAccountStatusesValue() accountStatuses {
	return accountStatuses{
		solved:    make(map[string]struct{}),
		attempted: make(map[string]struct{}),
		scores:    make(map[string]int),
	}
}

func cloneStatuses(in accountStatuses) accountStatuses {
	out := newAccountStatusesValue()
	for k := range in.solved {
		out.solved[k] = struct{}{}
	}
	for k := range in.attempted {
		out.attempted[k] = struct{}{}
	}
	for k, v := range in.scores {
		out.scores[k] = v
	}
	return out
}

func mergeStatuses(dst *accountStatuses, src accountStatuses) {
	if dst == nil {
		return
	}
	for k := range src.solved {
		dst.solved[k] = struct{}{}
	}
	for k := range src.attempted {
		dst.attempted[k] = struct{}{}
	}
	for k, v := range src.scores {
		if prev, ok := dst.scores[k]; !ok || v > prev {
			dst.scores[k] = v
		}
	}
}

func normalizeSite(site string) string {
	return strings.ToLower(strings.TrimSpace(site))
}

func alphabetLabel(idx int) string {
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
