package standings

import (
	"context"
	"encoding/json"
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
	// timed — посылки с временем по нормализованному URL задачи (для сайтов,
	// отдающих время: codeforces, informatics). Нужно для фильтрации по окну.
	timed map[string][]source.TimedSubmission
}

type preparedGroup struct {
	group    domain.GroupDefinition
	students []domain.Student
	// contests — только update=true: их пересобираем с нуля.
	contests []domain.Contest
	// allContests — все контесты группы (и update=false): по ним считаем
	// требуемые сайты и доску почёта, чтобы те оставались живыми.
	allContests []domain.Contest
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
		buildContests, allContests := b.resolveGroupContests(data, group)
		out = append(out, preparedGroup{
			group:       group,
			students:    students,
			contests:    buildContests,
			allContests: allContests,
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

// resolveGroupContests резолвит контесты группы и делит их на «пересобираемые»
// (update=true) и полный список (для сайтов и доски почёта).
func (b *Builder) resolveGroupContests(data *domain.SourceData, group domain.GroupDefinition) (buildContests, allContests []domain.Contest) {
	buildContests = make([]domain.Contest, 0, len(group.Contests))
	allContests = make([]domain.Contest, 0, len(group.Contests))
	for _, contestRef := range group.Contests {
		contest, ok := resolveGroupContestDef(data, contestRef)
		if !ok {
			b.logger.Printf("WARN group=%s unknown contest_id=%s", group.Slug, contestRef.ID)
			continue
		}
		allContests = append(allContests, contest)
		if contestRef.Update {
			buildContests = append(buildContests, contest)
		}
	}
	return buildContests, allContests
}

// resolveGroupContestDef резолвит ссылку/inline в определение контеста с учётом
// переопределения table_name на уровне группы. Возвращает (contest, найдено).
func resolveGroupContestDef(data *domain.SourceData, contestRef domain.GroupContestRef) (domain.Contest, bool) {
	var contest domain.Contest
	if contestRef.Inline != nil {
		contest = *contestRef.Inline
		if strings.TrimSpace(contest.ID) == "" {
			contest.ID = contestRef.ID
		}
	} else {
		resolved, ok := data.Contests[contestRef.ID]
		if !ok {
			return domain.Contest{}, false
		}
		contest = resolved
	}
	if contestRef.TableNames != nil {
		contest.TableNames = contestRef.TableNames
	}
	return contest, true
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
		for _, contest := range pg.allContests {
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

		if len(result.Timed) > 0 {
			out.timed[normalized] = append(out.timed[normalized], result.Timed...)
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

	now := time.Now().UTC()
	for _, contest := range pg.contests {
		switch contest.TypeOrDefault() {
		case domain.ContestTypeTasks:
			expanded := b.expandCodeforcesContestRefs(ctx, data, pg.group, contest, pg.students)
			generated := b.buildTaskContestStandings(contest, pg.students, statusByStudent, expanded)
			generated.GeneratedAt = &now
			out.Contests = append(out.Contests, generated)
		case domain.ContestTypeProvider:
			generated, err := b.buildProviderContestStandings(ctx, data, pg.group, contest, pg.students)
			if err != nil {
				b.logger.Printf("WARN group=%s contest_id=%s provider build failed; keep previous generated version if available: %v", pg.group.Slug, contest.ID, err)
				continue
			}
			generated.GeneratedAt = &now
			out.Contests = append(out.Contests, generated)
		default:
			return domain.GeneratedGroupStandings{}, fmt.Errorf("contest_id=%s unsupported contest_type=%s", contest.ID, contest.TypeOrDefault())
		}
	}
	out.SolvedSummarySites, out.SolvedSummary = b.buildGroupSolvedSummary(pg.allContests, pg.students, statusByStudent)

	return out, nil
}

func (b *Builder) buildGroupSolvedSummary(
	contests []domain.Contest,
	students []domain.Student,
	statusByStudent map[string]*accountStatuses,
) ([]string, []domain.GeneratedGroupSolvedSummaryRow) {
	groupSites := make(map[string]struct{})
	for _, contest := range contests {
		if contest.TypeOrDefault() != domain.ContestTypeTasks {
			continue
		}
		for _, subcontest := range contest.Subcontests {
			for _, rawTaskURL := range subcontest.Tasks {
				normalized := domain.NormalizeTaskURL(rawTaskURL)
				site, _, ok := b.sources.ResolveSiteByTaskURL(normalized)
				if !ok {
					continue
				}
				site = domain.NormalizeSite(site)
				if site == "" {
					continue
				}
				groupSites[site] = struct{}{}
			}
		}
	}

	if len(groupSites) == 0 {
		return nil, nil
	}

	sites := make([]string, 0, len(groupSites))
	for site := range groupSites {
		sites = append(sites, site)
	}
	sort.Strings(sites)

	siteIndex := make(map[string]int, len(sites))
	for i, site := range sites {
		siteIndex[site] = i
	}

	rows := make([]domain.GeneratedGroupSolvedSummaryRow, 0, len(students))
	siteByTaskURL := make(map[string]string)
	for _, student := range students {
		combined := statusByStudent[student.ID]
		if combined == nil {
			combined = newAccountStatuses()
		}

		row := domain.GeneratedGroupSolvedSummaryRow{
			StudentID:         student.ID,
			PublicName:        student.PublicName,
			SolvedCountBySite: make([]int, len(sites)),
		}
		for taskURL := range combined.solved {
			site, resolved := siteByTaskURL[taskURL]
			if !resolved {
				resolvedSite, _, ok := b.sources.ResolveSiteByTaskURL(taskURL)
				if ok {
					site = domain.NormalizeSite(resolvedSite)
				}
				siteByTaskURL[taskURL] = site
			}
			if site == "" {
				continue
			}
			row.TotalSolvedCount++
			if idx, ok := siteIndex[site]; ok {
				row.SolvedCountOnPageSites++
				row.SolvedCountBySite[idx]++
			}
		}
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalSolvedCount != rows[j].TotalSolvedCount {
			return rows[i].TotalSolvedCount > rows[j].TotalSolvedCount
		}
		return strings.ToLower(rows[i].PublicName) < strings.ToLower(rows[j].PublicName)
	})

	return sites, rows
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
	standings.TableNames = contest.TableNames
	standings.Materials = domain.NormalizeContestMaterials(contest.Materials)
	return standings, nil
}

// expandCodeforcesContestRefs для каждой ссылки на контест Codeforces в списке
// задач tasks-контеста делает по одному запросу contest.standings (через
// codeforces_contest провайдер, с его логикой gym/non-gym и fallback) и
// возвращает результат по contest_id. nil в значении — развернуть не удалось.
func (b *Builder) expandCodeforcesContestRefs(ctx context.Context, data *domain.SourceData, group domain.GroupDefinition, contest domain.Contest, students []domain.Student) map[int]*domain.GeneratedContestStandings {
	ids := make([]int, 0)
	seen := make(map[int]struct{})
	for _, subcontest := range contest.Subcontests {
		for _, rawTaskURL := range subcontest.Tasks {
			if cid, ok := source.ParseCodeforcesContestID(rawTaskURL); ok {
				if _, dup := seen[cid]; !dup {
					seen[cid] = struct{}{}
					ids = append(ids, cid)
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}

	provider, ok := b.sources.Provider(source.CodeforcesContestProviderID)
	if !ok {
		b.logger.Printf("WARN group=%s contest=%s codeforces contest provider not registered; contest refs skipped", group.Slug, contest.ID)
		return nil
	}

	out := make(map[int]*domain.GeneratedContestStandings, len(ids))
	for _, cid := range ids {
		synthetic := domain.Contest{
			ID:             fmt.Sprintf("%s#cf%d", contest.ID, cid),
			Title:          contest.Title,
			ScoreSystem:    contest.ScoreSystem,
			ContestType:    domain.ContestTypeProvider,
			Provider:       source.CodeforcesContestProviderID,
			ProviderConfig: json.RawMessage(fmt.Sprintf(`{"contest_id":%d,"show_unofficial":true}`, cid)),
		}
		res, err := provider.BuildStandings(ctx, source.ContestProviderInput{
			Source:   data,
			Group:    group,
			Contest:  synthetic,
			Students: students,
		})
		if err != nil {
			b.logger.Printf("WARN group=%s contest=%s expand codeforces contest %d failed: %v", group.Slug, contest.ID, cid, err)
			out[cid] = nil
			continue
		}
		expanded := res
		out[cid] = &expanded
	}
	return out
}

// expandedContestProblemURL строит ссылку на задачу из исходной ссылки на
// контест Codeforces, сохраняя её форму (group/contest/gym): <contestURL>/problem/<idx>.
func expandedContestProblemURL(contestURL, problemIndex string) string {
	base := strings.TrimRight(strings.TrimSpace(contestURL), "/")
	index := strings.TrimSpace(problemIndex)
	if base == "" || index == "" {
		return strings.TrimSpace(contestURL)
	}
	return base + "/problem/" + index
}

type taskColumn struct {
	fromContest   *domain.GeneratedContestStandings // != nil => столбец из развёрнутого CF-контеста
	problemIndex  int
	normalizedURL string // для обычной задачи
	useRealScores bool   // для обычной IOI-задачи
}

func (b *Builder) buildTaskContestStandings(contest domain.Contest, students []domain.Student, statusByStudent map[string]*accountStatuses, expanded map[int]*domain.GeneratedContestStandings) domain.GeneratedContestStandings {
	isIOI := contest.ScoreSystem.IsIOI()

	var windowStart, windowEnd time.Time
	windowActive := false
	if contest.StartTime != nil && contest.EndTime != nil {
		windowStart = contest.StartTime.UTC()
		windowEnd = contest.EndTime.UTC()
		windowActive = !windowEnd.Before(windowStart)
	}

	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		ScoreSystem: contest.ScoreSystem.Normalized(),
		ContestType: domain.ContestTypeTasks,
		TableNames:  contest.TableNames,
		Materials:   domain.NormalizeContestMaterials(contest.Materials),
		Subcontests: make([]domain.GeneratedSubcontest, 0, len(contest.Subcontests)),
		Tasks:       make([]domain.GeneratedTask, 0),
		Rows:        make([]domain.GeneratedRow, 0, len(students)),
	}

	columns := make([]taskColumn, 0)
	for _, subcontest := range contest.Subcontests {
		generatedSubcontest := domain.GeneratedSubcontest{
			Title: subcontest.Title,
			Tasks: make([]domain.GeneratedTask, 0, len(subcontest.Tasks)),
		}
		for _, rawTaskURL := range subcontest.Tasks {
			if cid, ok := source.ParseCodeforcesContestID(rawTaskURL); ok {
				contestStandings := expanded[cid]
				if contestStandings == nil {
					continue // развернуть не удалось — запись пропускаем
				}
				for problemIndex, problemTask := range contestStandings.Tasks {
					// Ссылку задачи строим из ИСХОДНОЙ ссылки на контест
					// (group/contest/gym — как вписал пользователь), а не из
					// формы, которую вернул провайдер, чтобы не терять префикс группы.
					problemURL := expandedContestProblemURL(rawTaskURL, problemTask.Label)
					task := domain.GeneratedTask{
						Label:         domain.AlphabetLabel(len(generatedSubcontest.Tasks)),
						URL:           problemURL,
						NormalizedURL: domain.NormalizeTaskURL(problemURL),
					}
					generatedSubcontest.Tasks = append(generatedSubcontest.Tasks, task)
					out.Tasks = append(out.Tasks, task)
					columns = append(columns, taskColumn{fromContest: contestStandings, problemIndex: problemIndex})
				}
				continue
			}

			normalized := domain.NormalizeTaskURL(rawTaskURL)
			task := domain.GeneratedTask{
				Label:         domain.AlphabetLabel(len(generatedSubcontest.Tasks)),
				URL:           strings.TrimSpace(rawTaskURL),
				NormalizedURL: normalized,
			}
			generatedSubcontest.Tasks = append(generatedSubcontest.Tasks, task)
			out.Tasks = append(out.Tasks, task)

			useRealScores := false
			if isIOI {
				_, client, ok := b.sources.ResolveSiteByTaskURL(normalized)
				if ok && client != nil && client.SupportsTaskScores() {
					useRealScores = true
				}
			}
			columns = append(columns, taskColumn{normalizedURL: normalized, useRealScores: useRealScores})
		}
		generatedSubcontest.TaskCount = len(generatedSubcontest.Tasks)
		out.Subcontests = append(out.Subcontests, generatedSubcontest)
	}

	// Строки развёрнутых контестов — по student_id.
	expandedRowByStudent := make(map[*domain.GeneratedContestStandings]map[string]*domain.GeneratedRow)
	for _, contestStandings := range expanded {
		if contestStandings == nil {
			continue
		}
		byStudent := make(map[string]*domain.GeneratedRow, len(contestStandings.Rows))
		for i := range contestStandings.Rows {
			byStudent[contestStandings.Rows[i].StudentID] = &contestStandings.Rows[i]
		}
		expandedRowByStudent[contestStandings] = byStudent
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
		if isIOI {
			row.Scores = make([]*int, len(out.Tasks))
		}
		upsolved := make([]bool, len(out.Tasks))
		hasUpsolved := false

		for i, col := range columns {
			status := domain.TaskStatusNone

			if col.fromContest != nil {
				if byStudent, ok := expandedRowByStudent[col.fromContest]; ok {
					if providerRow := byStudent[student.ID]; providerRow != nil && col.problemIndex < len(providerRow.Statuses) {
						status = providerRow.Statuses[col.problemIndex]
						if isIOI && col.problemIndex < len(providerRow.Scores) && providerRow.Scores[col.problemIndex] != nil {
							value := *providerRow.Scores[col.problemIndex]
							row.Scores[i] = &value
							row.TotalScore += value
						}
						if col.problemIndex < len(providerRow.Upsolved) && providerRow.Upsolved[col.problemIndex] {
							upsolved[i] = true
							hasUpsolved = true
						}
					}
				}
				row.Statuses[i] = status
				if status == domain.TaskStatusSolved {
					row.SolvedCount++
				}
				continue
			}

			// Окно контеста: для сайтов с временем посылок учитываем только окно,
			// после конца — дорешка. Нет данных о времени (ACMP) — падаем на
			// обычную логику (всё время).
			if windowActive {
				if st, sc, hasScore, up, hasData := windowedTaskResult(combined.timed[col.normalizedURL], windowStart, windowEnd, isIOI); hasData {
					row.Statuses[i] = st
					if st == domain.TaskStatusSolved {
						row.SolvedCount++
					}
					if isIOI && hasScore {
						value := sc
						row.Scores[i] = &value
						row.TotalScore += sc
					}
					if up {
						upsolved[i] = true
						hasUpsolved = true
					}
					continue
				}
			}

			if _, ok := combined.solved[col.normalizedURL]; ok {
				status = domain.TaskStatusSolved
				row.SolvedCount++
			} else if _, ok := combined.attempted[col.normalizedURL]; ok {
				status = domain.TaskStatusAttempted
			}
			row.Statuses[i] = status

			if isIOI {
				score, ok := resolveTaskScore(status, combined, col.normalizedURL, col.useRealScores)
				if ok {
					value := score
					row.Scores[i] = &value
					row.TotalScore += score
				}
			}
		}

		if hasUpsolved {
			row.Upsolved = upsolved
		}
		out.Rows = append(out.Rows, row)
	}

	sort.Slice(out.Rows, func(i, j int) bool {
		if isIOI {
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

	// Таблицу по набору задач строим сами, значит и места проставляем сами.
	assignTaskContestPlaces(out.Rows, isIOI)

	return out
}

// assignTaskContestPlaces проставляет места по уже отсортированным строкам.
// Равный ранг (одинаковый счёт/число решённых) получает один и тот же диапазон
// мест ("3-5"). Дорешка уже учтена в SolvedCount/TotalScore и здесь отдельно
// не выделяется (контесты по набору задач не различают время решения).
func assignTaskContestPlaces(rows []domain.GeneratedRow, isIOI bool) {
	sameRank := func(a, b domain.GeneratedRow) bool {
		if isIOI {
			return a.TotalScore == b.TotalScore
		}
		return a.SolvedCount == b.SolvedCount
	}

	i := 0
	for i < len(rows) {
		j := i + 1
		for j < len(rows) && sameRank(rows[i], rows[j]) {
			j++
		}
		if j-i == 1 {
			rows[i].Place = fmt.Sprintf("%d", i+1)
		} else {
			place := fmt.Sprintf("%d-%d", i+1, j)
			for k := i; k < j; k++ {
				rows[k].Place = place
			}
		}
		i = j
	}
}

// windowedTaskResult вычисляет статус/балл/пометку дорешки по посылкам с временем
// и окну контеста [start, end]. Посылки до start игнорируются, в окне идут в зачёт,
// после end — в дорешку. Для IOI показывается больший балл (окна или дорешки), и
// если победил балл дорешки — ячейка помечается дорешкой.
// hasData=false — нет посылок с временем (нужно упасть на обычную логику).
func windowedTaskResult(timed []source.TimedSubmission, start, end time.Time, isIOI bool) (status string, score int, hasScore bool, upsolved bool, hasData bool) {
	if len(timed) == 0 {
		return domain.TaskStatusNone, 0, false, false, false
	}

	inSolved, inAttempted := false, false
	afterSolved, afterAttempted := false, false
	inBest, inHas := 0, false
	afterBest, afterHas := 0, false

	for _, sub := range timed {
		if sub.At.Before(start) {
			continue // до начала — игнорируем
		}
		subScore := 0
		if sub.Score != nil {
			subScore = domain.ClampScore(*sub.Score)
		}
		if !sub.At.After(end) { // в окне [start, end]
			inAttempted = true
			if sub.Solved {
				inSolved = true
			}
			if !inHas || subScore > inBest {
				inBest, inHas = subScore, true
			}
		} else { // после окончания — дорешка
			afterAttempted = true
			if sub.Solved {
				afterSolved = true
			}
			if !afterHas || subScore > afterBest {
				afterBest, afterHas = subScore, true
			}
		}
	}

	// Статус и базовая пометка дорешки — по факту решения/попытки.
	switch {
	case inSolved:
		status, upsolved = domain.TaskStatusSolved, false
	case afterSolved:
		status, upsolved = domain.TaskStatusSolved, true
	case inAttempted:
		status, upsolved = domain.TaskStatusAttempted, false
	case afterAttempted:
		status, upsolved = domain.TaskStatusAttempted, true
	default:
		status, upsolved = domain.TaskStatusNone, false
	}

	if isIOI && status != domain.TaskStatusNone {
		inScore := 0
		if inHas {
			inScore = inBest
		}
		afterScore := 0
		if afterHas {
			afterScore = afterBest
		}
		// Балл дорешки показываем, только если он строго больше балла в окне.
		if afterScore > inScore {
			score, upsolved = afterScore, true
		} else {
			score, upsolved = inScore, false
		}
		hasScore = true
	}

	return status, score, hasScore, upsolved, true
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
		timed:     make(map[string][]source.TimedSubmission),
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
	for key, subs := range src.timed {
		if dst.timed == nil {
			dst.timed = make(map[string][]source.TimedSubmission)
		}
		dst.timed[key] = append(dst.timed[key], subs...)
	}
}
