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
}

type inflightCall struct {
	done chan struct{}
	res  accountStatuses
	err  error
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

	out := domain.GeneratedGroupStandings{
		GroupSlug:  group.Slug,
		GroupTitle: group.Title,
		Contests:   make([]domain.GeneratedContestStandings, 0, len(contests)),
	}

	for _, contest := range contests {
		generatedContest := b.buildContestStandings(contest, students, statusByStudent)
		out.Contests = append(out.Contests, generatedContest)
	}

	return out, nil
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

func (b *StandingsBuilder) buildContestStandings(contest domain.Contest, students []domain.Student, statusByStudent map[string]*accountStatuses) domain.GeneratedContestStandings {
	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		Subcontests: make([]domain.GeneratedSubcontest, 0, len(contest.Subcontests)),
		Tasks:       make([]domain.GeneratedTask, 0),
		Rows:        make([]domain.GeneratedRow, 0, len(students)),
	}

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
			Statuses:    make([]string, len(out.Tasks)),
			SolvedCount: 0,
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
		}

		out.Rows = append(out.Rows, row)
	}

	sort.Slice(out.Rows, func(i, j int) bool {
		if out.Rows[i].SolvedCount != out.Rows[j].SolvedCount {
			return out.Rows[i].SolvedCount > out.Rows[j].SolvedCount
		}
		return strings.ToLower(out.Rows[i].FullName) < strings.ToLower(out.Rows[j].FullName)
	})

	return out
}

func (b *StandingsBuilder) collectStudentStatuses(ctx context.Context, students []domain.Student) (map[string]*accountStatuses, error) {
	result := make(map[string]*accountStatuses, len(students))
	for _, student := range students {
		result[student.ID] = newAccountStatuses()
	}

	type accountFetchResult struct {
		studentID string
		statuses  accountStatuses
	}

	resultsCh := make(chan accountFetchResult)
	sem := make(chan struct{}, b.maxConcurrent)
	wg := sync.WaitGroup{}

	for _, student := range students {
		for _, account := range student.Accounts {
			account := account
			studentID := student.ID
			if strings.TrimSpace(account.Site) == "" || strings.TrimSpace(account.AccountID) == "" {
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

				statuses, err := b.fetchAccountStatuses(ctx, account.Site, account.AccountID)
				if err != nil {
					b.logger.Printf("WARN student_id=%s site=%s account_id=%s fetch error: %v", studentID, account.Site, account.AccountID, err)
					return
				}

				select {
				case resultsCh <- accountFetchResult{studentID: studentID, statuses: statuses}:
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
		agg := result[res.studentID]
		for taskURL := range res.statuses.solved {
			agg.solved[taskURL] = struct{}{}
		}
		for taskURL := range res.statuses.attempted {
			agg.attempted[taskURL] = struct{}{}
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (b *StandingsBuilder) fetchAccountStatuses(ctx context.Context, site string, accountID string) (accountStatuses, error) {
	site = strings.ToLower(strings.TrimSpace(site))
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

	solvedRaw, attemptedRaw, err := client.FetchUserStatuses(ctx, accountID)
	if err != nil {
		return accountStatuses{}, err
	}

	out := newAccountStatusesValue()
	for _, raw := range solvedRaw {
		normalized := domain.NormalizeTaskURL(raw)
		if normalized == "" {
			continue
		}
		out.solved[normalized] = struct{}{}
	}
	for _, raw := range attemptedRaw {
		normalized := domain.NormalizeTaskURL(raw)
		if normalized == "" {
			continue
		}
		out.attempted[normalized] = struct{}{}
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
	return out
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
