package standings

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
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

type preparedGroup struct {
	group         domain.GroupDefinition
	students      []domain.Student
	contests      []domain.Contest
	requiredSites map[string]struct{}
}

type Builder struct {
	sources       *source.Registry
	logger        *log.Logger
	maxConcurrent int
	cache         *ttlCache[accountStatuses]

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall
}

func NewBuilder(sources *source.Registry, logger *log.Logger, maxConcurrent int, cacheTTL time.Duration) *Builder {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	if cacheTTL <= 0 {
		cacheTTL = 5 * time.Minute
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
		cache:         newTTLCache[accountStatuses](cacheTTL),
		inflight:      make(map[string]*inflightCall),
	}
}

func (b *Builder) BuildGroupStandings(ctx context.Context, data *domain.SourceData, group domain.GroupDefinition) (domain.GeneratedGroupStandings, error) {
	all, err := b.BuildGroupsStandings(ctx, data, []domain.GroupDefinition{group})
	if err != nil {
		return domain.GeneratedGroupStandings{}, err
	}
	standings, ok := all[group.Slug]
	if !ok {
		return domain.GeneratedGroupStandings{}, fmt.Errorf("group %q not built", group.Slug)
	}
	return standings, nil
}

func (b *Builder) BuildGroupsStandings(ctx context.Context, data *domain.SourceData, groups []domain.GroupDefinition) (map[string]domain.GeneratedGroupStandings, error) {
	if data == nil {
		return nil, fmt.Errorf("source data is nil")
	}

	prepared := b.prepareGroups(data, groups)
	if len(prepared) == 0 {
		return map[string]domain.GeneratedGroupStandings{}, nil
	}

	requiredSitesByStudent := b.buildRequiredSitesByStudent(prepared)
	b.logger.Printf("INFO selected user-site pairs for fetch: %d", countStudentSitePairs(requiredSitesByStudent))

	statusByStudentSite, err := b.collectStudentStatusesBySiteSelection(ctx, data, requiredSitesByStudent)
	if err != nil {
		return nil, err
	}

	combinedByStudent := b.buildCombinedStatusesByStudent(uniqueStudentsFromPrepared(prepared), statusByStudentSite)

	result := make(map[string]domain.GeneratedGroupStandings, len(prepared))
	for _, pg := range prepared {
		statusByStudent := b.pickCombinedStatuses(pg.students, combinedByStudent)
		standings, buildErr := b.buildGroupStandingsPrepared(ctx, data, pg.group, pg.contests, pg.students, statusByStudent)
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
		requiredSites := b.collectRequiredSitesFromContests(contests)
		out = append(out, preparedGroup{
			group:         group,
			students:      students,
			contests:      contests,
			requiredSites: requiredSites,
		})
	}
	return out
}

func (b *Builder) collectRequiredSitesFromContests(contests []domain.Contest) map[string]struct{} {
	out := make(map[string]struct{})
	for _, contest := range contests {
		if contest.TypeOrDefault() != domain.ContestTypeTasks {
			continue
		}
		for _, sc := range contest.Subcontests {
			for _, taskURL := range sc.Tasks {
				normalized := domain.NormalizeTaskURL(taskURL)
				site, _, ok := b.sources.ResolveSiteByTaskURL(normalized)
				if !ok || site == "" {
					continue
				}
				out[domain.NormalizeSite(site)] = struct{}{}
			}
		}
	}
	return out
}

func (b *Builder) buildRequiredSitesByStudent(prepared []preparedGroup) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, pg := range prepared {
		for _, student := range pg.students {
			sitesSet := out[student.ID]
			if sitesSet == nil {
				sitesSet = make(map[string]struct{})
				out[student.ID] = sitesSet
			}
			for site := range pg.requiredSites {
				sitesSet[site] = struct{}{}
			}
		}
	}
	return out
}

func countStudentSitePairs(requiredSitesByStudent map[string]map[string]struct{}) int {
	pairs := 0
	for _, sitesSet := range requiredSitesByStudent {
		pairs += len(sitesSet)
	}
	return pairs
}

func uniqueStudentsFromPrepared(prepared []preparedGroup) []domain.Student {
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

func (b *Builder) buildGroupStandingsPrepared(
	ctx context.Context,
	data *domain.SourceData,
	group domain.GroupDefinition,
	contests []domain.Contest,
	students []domain.Student,
	statusByStudent map[string]*accountStatuses,
) (domain.GeneratedGroupStandings, error) {
	out := domain.GeneratedGroupStandings{
		GroupSlug:  group.Slug,
		GroupTitle: group.Title,
		Contests:   make([]domain.GeneratedContestStandings, 0, len(contests)),
	}

	for _, contest := range contests {
		typ := contest.TypeOrDefault()
		switch typ {
		case domain.ContestTypeTasks:
			out.Contests = append(out.Contests, b.buildTaskContestStandings(contest, students, statusByStudent))
		case domain.ContestTypeProvider:
			generated, err := b.buildProviderContestStandings(ctx, data, group, contest, students)
			if err != nil {
				b.logger.Printf("WARN group=%s contest_id=%s provider build failed; keep previous generated version if available: %v", group.Slug, contest.ID, err)
				continue
			}
			out.Contests = append(out.Contests, generated)
		default:
			return domain.GeneratedGroupStandings{}, fmt.Errorf("contest_id=%s unsupported contest_type=%s", contest.ID, typ)
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
	return standings, nil
}

func (b *Builder) buildTaskContestStandings(contest domain.Contest, students []domain.Student, statusByStudent map[string]*accountStatuses) domain.GeneratedContestStandings {
	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		Olympiad:    contest.Olympiad,
		ContestType: domain.ContestTypeTasks,
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
				Label:         domain.AlphabetLabel(i),
				URL:           strings.TrimSpace(taskURL),
				NormalizedURL: normalized,
			}
			genSub.Tasks = append(genSub.Tasks, task)
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
			PublicName:  student.PublicName,
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

func (b *Builder) collectStudentStatusesBySiteSelection(ctx context.Context, data *domain.SourceData, requiredSitesByStudent map[string]map[string]struct{}) (map[string]map[string]*accountStatuses, error) {
	result := make(map[string]map[string]*accountStatuses, len(requiredSitesByStudent))

	resultsCh := make(chan accountFetchResult)
	sem := make(chan struct{}, b.maxConcurrent)
	wg := sync.WaitGroup{}

	seenFetches := make(map[string]struct{})
	for studentID, requiredSites := range requiredSitesByStudent {
		if len(requiredSites) == 0 {
			continue
		}

		student, ok := data.Students[studentID]
		if !ok {
			b.logger.Printf("WARN unknown student_id=%s while collecting statuses", studentID)
			continue
		}

		for _, account := range student.Accounts {
			site := domain.NormalizeSite(account.Site)
			if _, need := requiredSites[site]; !need {
				continue
			}

			accountID := strings.TrimSpace(account.AccountID)
			if accountID == "" {
				continue
			}

			fetchKey := studentID + "|" + site + "|" + accountID
			if _, already := seenFetches[fetchKey]; already {
				continue
			}
			seenFetches[fetchKey] = struct{}{}

			wg.Add(1)
			go func(studentID string, site string, accountID string) {
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
			}(studentID, site, accountID)
		}
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	for res := range resultsCh {
		studentSites := result[res.studentID]
		if studentSites == nil {
			studentSites = make(map[string]*accountStatuses)
			result[res.studentID] = studentSites
		}
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

func (b *Builder) buildCombinedStatusesByStudent(students []domain.Student, statusByStudentSite map[string]map[string]*accountStatuses) map[string]*accountStatuses {
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

func (b *Builder) pickCombinedStatuses(students []domain.Student, combinedByStudent map[string]*accountStatuses) map[string]*accountStatuses {
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

func (b *Builder) fetchAccountStatuses(ctx context.Context, site string, accountID string) (accountStatuses, error) {
	site = domain.NormalizeSite(site)
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

func (b *Builder) loadFromSite(ctx context.Context, site string, accountID string) (accountStatuses, error) {
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

func (b *Builder) acquireInflight(cacheKey string) *inflightCall {
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()

	if call, ok := b.inflight[cacheKey]; ok {
		return call
	}

	b.inflight[cacheKey] = &inflightCall{done: make(chan struct{})}
	return nil
}

func (b *Builder) resolveInflight(cacheKey string, res accountStatuses, err error) {
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
