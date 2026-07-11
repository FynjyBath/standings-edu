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
	// okSolved — задачи, решённые полным OK (а не только «зачтено»). Задача,
	// которая есть в solved, но не в okSolved, решена статусом «зачтено» и
	// помечается в таблице.
	okSolved map[string]struct{}
	scores   map[string]int
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

func (b *Builder) BuildGroupsStandings(ctx context.Context, data *domain.SourceData, groups []domain.GroupDefinition) (map[string]domain.GeneratedGroupStandings, map[string]*domain.GeneratedStudentProfile, error) {
	if data == nil {
		return nil, nil, fmt.Errorf("source data is nil")
	}

	prepared := b.prepareGroups(data, groups)
	if len(prepared) == 0 {
		return map[string]domain.GeneratedGroupStandings{}, map[string]*domain.GeneratedStudentProfile{}, nil
	}

	requiredSites := b.collectRequiredTaskSites(prepared)
	students := uniqueStudents(prepared)
	statusByStudent, err := b.collectStudentsTaskStatuses(ctx, students, requiredSites)
	if err != nil {
		return nil, nil, err
	}

	result := make(map[string]domain.GeneratedGroupStandings, len(prepared))
	for _, pg := range prepared {
		standings, buildErr := b.buildGroupStandings(ctx, data, pg, statusByStudent)
		if buildErr != nil {
			return nil, nil, fmt.Errorf("group=%s build standings: %w", pg.group.Slug, buildErr)
		}
		result[pg.group.Slug] = standings
	}

	profiles := b.buildStudentProfiles(students, statusByStudent, time.Now().UTC())
	return result, profiles, nil
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
	// Правило для всех поведенческих параметров: значение из записи группы
	// (если задано) переопределяет значение из определения контеста.
	if contestRef.TableNames != nil {
		contest.TableNames = contestRef.TableNames
	}
	if contestRef.StartTime != nil {
		contest.StartTime = contestRef.StartTime
	}
	if contestRef.EndTime != nil {
		contest.EndTime = contestRef.EndTime
	}
	if contestRef.ZeroPenalty != nil {
		contest.ZeroPenalty = *contestRef.ZeroPenalty
	}
	if contestRef.SummaryTotalOnly != nil {
		contest.SummaryTotalOnly = *contestRef.SummaryTotalOnly
	}
	if contestRef.Hidden != nil {
		contest.Hidden = *contestRef.Hidden
	}
	// Заморозка: переопределение группы (в т.ч. "none" — выключить), иначе
	// из определения; момент считается от итогового окна.
	freeze := contestRef.Freeze
	if freeze == nil {
		// Валидность строки проверена при загрузке contests.json.
		freeze, _ = domain.ParseFreezeSpec(contest.Freeze)
	}
	contest.FreezeTime = freeze.FreezeMoment(contest.StartTime, contest.EndTime)
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
					site, client, ok := b.sources.ResolveSiteByTaskURL(normalized)
					if !ok {
						continue
					}
					out[domain.NormalizeSite(site)] = struct{}{}
					// Клиентам, которым нужно знать ссылки заранее (ejudge —
					// contest_id для забора прогонов), сообщаем ссылку задачи.
					if observer, ok := client.(source.TaskURLObserver); ok {
						observer.ObserveTaskURL(normalized)
					}
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
			// «Зачтено» (result.Accepted) не добавляет в okSolved — по нему потом
			// отличаем полный OK от «зачтено».
			if !result.Accepted {
				out.okSolved[normalized] = struct{}{}
			}
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
			statementRefs := b.expandInformaticsStatementRefs(ctx, pg.group, contest)
			ejudgeRefs := b.expandEjudgeContestRefs(ctx, pg.group, contest)
			generated := b.buildTaskContestStandings(contest, pg.students, statusByStudent, expanded, statementRefs, ejudgeRefs)
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

	domain.SortSolvedSummary(rows)

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

// expandInformaticsStatementRefs для каждой ссылки на сборник informatics
// (mod/statements/view.php?id=… без chapterid) в списке задач tasks-контеста
// читает страницу сборника и возвращает по statement_id упорядоченный список
// ссылок на отдельные задачи (в форме …?chapterid=<N>#1 — ссылка на саму задачу,
// без сборника). nil в значении — развернуть не удалось (задачи пропускаются).
// В отличие от Codeforces отдельная таблица не строится: результаты берутся из
// уже собранных посылок ученика по обычному сопоставлению URL.
func (b *Builder) expandInformaticsStatementRefs(ctx context.Context, group domain.GroupDefinition, contest domain.Contest) map[int][]string {
	ids := make([]int, 0)
	rawByID := make(map[int]string)
	seen := make(map[int]struct{})
	for _, subcontest := range contest.Subcontests {
		for _, rawTaskURL := range subcontest.Tasks {
			if sid, ok := source.ParseInformaticsStatementID(rawTaskURL); ok {
				if _, dup := seen[sid]; !dup {
					seen[sid] = struct{}{}
					ids = append(ids, sid)
					rawByID[sid] = rawTaskURL
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}

	out := make(map[int][]string, len(ids))
	for _, sid := range ids {
		_, client, ok := b.sources.ResolveSiteByTaskURL(rawByID[sid])
		expander, canExpand := client.(source.StatementExpander)
		if !ok || !canExpand {
			b.logger.Printf("WARN group=%s contest=%s: informatics statement expander unavailable; statement id=%d skipped", group.Slug, contest.ID, sid)
			out[sid] = nil
			continue
		}
		problems, err := expander.FetchStatementProblems(ctx, sid)
		if err != nil {
			b.logger.Printf("WARN group=%s contest=%s expand informatics statement %d failed: %v", group.Slug, contest.ID, sid, err)
			out[sid] = nil
			continue
		}
		urls := make([]string, 0, len(problems))
		for _, problem := range problems {
			if problem.ChapterID <= 0 {
				continue
			}
			urls = append(urls, fmt.Sprintf("https://informatics.msk.ru/mod/statements/view.php?chapterid=%d#1", problem.ChapterID))
		}
		out[sid] = urls
	}
	return out
}

// expandEjudgeContestRefs для каждой ссылки на контест ejudge (new-client?
// contest_id=… без prob_id) в списке задач возвращает по нормализованной ссылке
// контеста упорядоченный список ссылок на его задачи (…?contest_id=N&prob_id=M).
// nil в значении — развернуть не удалось (задачи пропускаются). Как и informatics,
// отдельная таблица не строится: результаты берутся из уже собранных прогонов
// ученика по обычному сопоставлению URL. Ключ — нормализованная ссылка контеста
// (учитывает хост, поэтому разные экземпляры ejudge не путаются).
func (b *Builder) expandEjudgeContestRefs(ctx context.Context, group domain.GroupDefinition, contest domain.Contest) map[string][]string {
	keys := make([]string, 0)
	rawByKey := make(map[string]string)
	seen := make(map[string]struct{})
	for _, subcontest := range contest.Subcontests {
		for _, rawTaskURL := range subcontest.Tasks {
			parsed, ok := domain.ParseEjudgeTaskURL(rawTaskURL)
			if !ok || parsed.ProbID != 0 {
				continue // не ejudge или ссылка на конкретную задачу
			}
			key := domain.NormalizeTaskURL(rawTaskURL)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
			rawByKey[key] = rawTaskURL
		}
	}
	if len(keys) == 0 {
		return nil
	}

	out := make(map[string][]string, len(keys))
	for _, key := range keys {
		raw := rawByKey[key]
		parsed, _ := domain.ParseEjudgeTaskURL(raw)
		_, client, ok := b.sources.ResolveSiteByTaskURL(raw)
		expander, canExpand := client.(source.EjudgeContestExpander)
		if !ok || !canExpand {
			b.logger.Printf("WARN group=%s contest=%s: ejudge expander unavailable for %s; contest %d skipped", group.Slug, contest.ID, raw, parsed.ContestID)
			out[key] = nil
			continue
		}
		problems, err := expander.FetchContestProblems(ctx, parsed.ContestID)
		if err != nil {
			b.logger.Printf("WARN group=%s contest=%s expand ejudge contest %d failed: %v", group.Slug, contest.ID, parsed.ContestID, err)
			out[key] = nil
			continue
		}
		urls := make([]string, 0, len(problems))
		for _, problem := range problems {
			if problem.ProbID <= 0 {
				continue
			}
			urls = append(urls, fmt.Sprintf("https://%s/new-client?contest_id=%d&prob_id=%d", parsed.Host, parsed.ContestID, problem.ProbID))
		}
		out[key] = urls
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

// singleTaskLink возвращает единственную ссылку-задачу контеста (по всем
// подконтестам), если она ровно одна. Это и есть «контест добавлен одной
// ссылкой» (например informatics-сборник, разворачивающийся в задачи).
func singleTaskLink(contest domain.Contest) (string, bool) {
	found := ""
	count := 0
	for _, sc := range contest.Subcontests {
		for _, t := range sc.Tasks {
			if t = strings.TrimSpace(t); t != "" {
				count++
				if count > 1 {
					return "", false
				}
				found = t
			}
		}
	}
	return found, count == 1
}

// informaticsBaseURL — настроенный base_url informatics (для переписывания хоста
// видимых ссылок). Пусто — informatics не зарегистрирован.
func (b *Builder) informaticsBaseURL() string {
	client, ok := b.sources.Site("informatics")
	if !ok || client == nil {
		return ""
	}
	provider, ok := client.(interface{ BaseURL() string })
	if !ok {
		return ""
	}
	return provider.BaseURL()
}

// linkableAccounts — account_id ученика по сайтам, для которых умеем строить
// ссылку на список его посылок по задаче. Пока только informatics (её user_id).
// Не включаем остальные сайты, чтобы не публиковать лишние идентификаторы.
func linkableAccounts(accounts []domain.Account) map[string]string {
	var out map[string]string
	for _, a := range accounts {
		site := domain.NormalizeSite(a.Site)
		if site != "informatics" {
			continue
		}
		id := strings.TrimSpace(a.AccountID)
		if id == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, 1)
		}
		if _, exists := out[site]; !exists {
			out[site] = id
		}
	}
	return out
}

func (b *Builder) buildTaskContestStandings(contest domain.Contest, students []domain.Student, statusByStudent map[string]*accountStatuses, expanded map[int]*domain.GeneratedContestStandings, statementRefs map[int][]string, ejudgeRefs map[string][]string) domain.GeneratedContestStandings {
	isIOI := contest.ScoreSystem.IsIOI()

	var windowStart, windowEnd time.Time
	windowActive := false
	if contest.StartTime != nil && contest.EndTime != nil {
		windowStart = contest.StartTime.UTC()
		windowEnd = contest.EndTime.UTC()
		windowActive = !windowEnd.Before(windowStart)
	}

	// Заморозка: в таблицу входят только посылки до момента заморозки, всё
	// позже (включая дорешку) полностью скрыто до разморозки и перегенерации.
	frozen := false
	if windowActive && contest.FreezeTime != nil {
		windowEnd = contest.FreezeTime.UTC()
		frozen = true
	}

	// Хост, под который переписываем все видимые informatics-ссылки (task URL,
	// материалы) — из base_url кредов informatics. Пусто — informatics не
	// настроен, ссылки остаются как введены.
	informaticsBase := b.informaticsBaseURL()

	materials := domain.NormalizeContestMaterials(contest.Materials)
	for i := range materials {
		materials[i].URL = domain.RewriteInformaticsHost(materials[i].URL, informaticsBase)
	}

	out := domain.GeneratedContestStandings{
		ID:               contest.ID,
		Title:            contest.Title,
		ScoreSystem:      contest.ScoreSystem.Normalized(),
		ContestType:      domain.ContestTypeTasks,
		TableNames:       contest.TableNames,
		Materials:        materials,
		StartTime:        contest.StartTime,
		EndTime:          contest.EndTime,
		SummaryTotalOnly: contest.SummaryTotalOnly,
		Hidden:           contest.Hidden,
		ShortName:        strings.TrimSpace(contest.ShortName),
		Subcontests:      make([]domain.GeneratedSubcontest, 0, len(contest.Subcontests)),
		Tasks:            make([]domain.GeneratedTask, 0),
		Rows:             make([]domain.GeneratedRow, 0, len(students)),
	}
	if frozen {
		out.FrozenAt = contest.FreezeTime
	}
	// Контест «только сумма», добавленный ровно одной informatics-ссылкой: по ней
	// в сводной колонке суммы дадим ссылку на все посылки ученика по контесту.
	if out.SummaryTotalOnly {
		if single, ok := singleTaskLink(contest); ok && domain.IsInformaticsURL(single) {
			out.SourceURL = domain.RewriteInformaticsHost(single, informaticsBase)
		}
	}
	// Штраф за задачу без баллов — только для табличек с баллами.
	zeroPenalty := 0
	if isIOI && contest.ZeroPenalty > 0 {
		zeroPenalty = contest.ZeroPenalty
		out.ZeroPenalty = zeroPenalty
	}

	columns := make([]taskColumn, 0)
	for _, subcontest := range contest.Subcontests {
		generatedSubcontest := domain.GeneratedSubcontest{
			Title: subcontest.Title,
			Tasks: make([]domain.GeneratedTask, 0, len(subcontest.Tasks)),
		}
		// addNormalTask добавляет обычную задачу-колонку по её ссылке (результат
		// берётся из посылок ученика по сопоставлению normalized_url).
		addNormalTask := func(rawURL string) {
			normalized := domain.NormalizeTaskURL(rawURL)
			task := domain.GeneratedTask{
				Label:         domain.AlphabetLabel(len(generatedSubcontest.Tasks)),
				URL:           domain.RewriteInformaticsHost(rawURL, informaticsBase),
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

			// Сборник informatics: разворачиваем в отдельные задачи-колонки
			// (ссылка на саму задачу, без сборника). Каждая — обычная задача.
			if sid, ok := source.ParseInformaticsStatementID(rawTaskURL); ok {
				for _, problemURL := range statementRefs[sid] {
					addNormalTask(problemURL)
				}
				continue
			}

			// Контест ejudge (contest_id без prob_id): разворачиваем в задачи.
			if parsed, ok := domain.ParseEjudgeTaskURL(rawTaskURL); ok && parsed.ProbID == 0 {
				for _, problemURL := range ejudgeRefs[domain.NormalizeTaskURL(rawTaskURL)] {
					addNormalTask(problemURL)
				}
				continue
			}

			addNormalTask(rawTaskURL)
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
			Accounts:    linkableAccounts(student.Accounts),
		}
		practice := make([]*int, len(out.Tasks))
		hasPractice := false
		if isIOI {
			row.Scores = make([]*int, len(out.Tasks))
		}
		upsolved := make([]bool, len(out.Tasks))
		hasUpsolved := false
		acceptedMarks := make([]bool, len(out.Tasks))
		hasAccepted := false
		zeros := 0 // задачи без баллов (пустые или с нулём) — для штрафа

		// addScore учитывает вклад задачи в сумму: максимум из основного балла
		// и дорешки; обе части nil или ноль — задача «нулевая» (штраф).
		addScore := func(i int, main, pract *int) {
			if !isIOI {
				return
			}
			row.Scores[i] = main
			if pract != nil {
				practice[i] = pract
				hasPractice = true
			}
			contribution := 0
			if main != nil {
				contribution = *main
			}
			if pract != nil && *pract > contribution {
				contribution = *pract
			}
			row.TotalScore += contribution
			if contribution <= 0 {
				zeros++
			}
		}

		for i, col := range columns {
			status := domain.TaskStatusNone

			if col.fromContest != nil {
				var providerScore *int
				if byStudent, ok := expandedRowByStudent[col.fromContest]; ok {
					if providerRow := byStudent[student.ID]; providerRow != nil && col.problemIndex < len(providerRow.Statuses) {
						status = providerRow.Statuses[col.problemIndex]
						if isIOI && col.problemIndex < len(providerRow.Scores) && providerRow.Scores[col.problemIndex] != nil {
							value := *providerRow.Scores[col.problemIndex]
							providerScore = &value
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
				addScore(i, providerScore, nil)
				continue
			}

			// Окно контеста: для сайтов с временем посылок учитываем только окно,
			// после конца — дорешка (при заморозке всё после момента заморозки
			// скрывается полностью). Нет данных о времени (ACMP) — падаем на
			// обычную логику (всё время).
			if windowActive {
				// У ejudge рамку даёт сам OK и ничто его не перебивает; у
				// informatics/остальных полный OK снимает рамку «зачтено».
				_, isEjudge := domain.ParseEjudgeTaskURL(col.normalizedURL)
				suppressBorder := !isEjudge
				if st, mainSc, practSc, up, acc, hasData := windowedTaskResult(combined.timed[col.normalizedURL], windowStart, windowEnd, isIOI, frozen, suppressBorder); hasData {
					row.Statuses[i] = st
					if st == domain.TaskStatusSolved {
						row.SolvedCount++
					}
					addScore(i, mainSc, practSc)
					if up {
						upsolved[i] = true
						hasUpsolved = true
					}
					if acc {
						acceptedMarks[i] = true
						hasAccepted = true
					}
					continue
				}
			}

			if _, ok := combined.solved[col.normalizedURL]; ok {
				status = domain.TaskStatusSolved
				row.SolvedCount++
				// Решено, но без полного OK → «зачтено».
				if _, okFull := combined.okSolved[col.normalizedURL]; !okFull {
					acceptedMarks[i] = true
					hasAccepted = true
				}
			} else if _, ok := combined.attempted[col.normalizedURL]; ok {
				status = domain.TaskStatusAttempted
			}
			row.Statuses[i] = status

			if isIOI {
				var mainSc *int
				if score, ok := resolveTaskScore(status, combined, col.normalizedURL, col.useRealScores); ok {
					value := score
					mainSc = &value
				}
				addScore(i, mainSc, nil)
			}
		}

		if hasUpsolved {
			row.Upsolved = upsolved
		}
		if hasAccepted {
			row.Accepted = acceptedMarks
		}
		if hasPractice {
			row.PracticeScores = practice
		}
		if zeroPenalty > 0 {
			penalty := zeros * zeroPenalty
			row.Penalty = &penalty
			row.TotalScore -= penalty
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

	// Для замороженного контеста считаем и полную версию строк (просмотр по
	// токену группы): та же сборка без заморозки — сеть не трогается, только
	// повторный проход по уже скачанным посылкам.
	if frozen {
		fullContest := contest
		fullContest.FreezeTime = nil
		full := b.buildTaskContestStandings(fullContest, students, statusByStudent, expanded, statementRefs, ejudgeRefs)
		out.RowsFull = full.Rows
	}

	return out
}

// assignTaskContestPlaces проставляет места по уже отсортированным строкам.
// Равный ранг (одинаковый счёт/число решённых) получает один и тот же диапазон
// мест ("3-5"). Дорешка уже учтена в SolvedCount/TotalScore и здесь отдельно
// не выделяется (контесты по набору задач не различают время решения).
func assignTaskContestPlaces(rows []domain.GeneratedRow, isIOI bool) {
	domain.AssignContestPlaces(rows, isIOI)
}

// windowedTaskResult вычисляет статус/балл/пометку дорешки по посылкам с временем
// и окну контеста [start, end]. Посылки до start игнорируются, в окне идут в зачёт,
// после end — в дорешку. Для IOI показывается больший балл (окна или дорешки), и
// если победил балл дорешки — ячейка помечается дорешкой.
// hasData=false — нет посылок с временем (нужно упасть на обычную логику).
// windowedTaskResult считает результат задачи по посылкам с временем. Для IOI
// основной балл (в окне) и балл дорешки возвращаются раздельно: mainScore —
// лучший в окне (nil — в окне не сдавал), practiceScore — лучший после конца,
// только если он строго больше основного. frozen — таблица заморожена: end
// здесь момент заморозки, и всё после него (включая дорешку) игнорируется
// полностью, чтобы результаты не протекали до разморозки.
// suppressBorder=true (informatics): жёлтая рамка «зачтено» снимается, если есть
// полный OK (solved без Accepted). suppressBorder=false (ejudge): рамку даёт сам
// Accepted-вердикт (OK), и ничто его не перебивает.
func windowedTaskResult(timed []source.TimedSubmission, start, end time.Time, isIOI bool, frozen bool, suppressBorder bool) (status string, mainScore, practiceScore *int, upsolved bool, accepted bool, hasData bool) {
	if len(timed) == 0 {
		return domain.TaskStatusNone, nil, nil, false, false, false
	}

	inSolved, inAttempted := false, false
	afterSolved, afterAttempted := false, false
	inOKSolved, afterOKSolved := false, false
	inBorder, afterBorder := false, false
	inBest, inHas := 0, false
	afterBest, afterHas := 0, false

	for _, sub := range timed {
		if sub.At.Before(start) {
			continue // до начала — игнорируем
		}
		if frozen && sub.At.After(end) {
			continue // заморозка: посылки после момента заморозки скрыты
		}
		subScore := 0
		if sub.Score != nil {
			subScore = domain.ClampScore(*sub.Score)
		}
		if !sub.At.After(end) { // в окне [start, end]
			inAttempted = true
			if sub.Solved {
				inSolved = true
				if sub.Accepted {
					inBorder = true
				} else {
					inOKSolved = true
				}
			}
			if !inHas || subScore > inBest {
				inBest, inHas = subScore, true
			}
		} else { // после окончания — дорешка
			afterAttempted = true
			if sub.Solved {
				afterSolved = true
				if sub.Accepted {
					afterBorder = true
				} else {
					afterOKSolved = true
				}
			}
			if !afterHas || subScore > afterBest {
				afterBest, afterHas = subScore, true
			}
		}
	}

	// Рамка: есть вердикт-рамка (Accepted) и (для informatics) его не перебил
	// полный OK. У отображаемого решения — основного в окне, иначе дорешки.
	if inSolved {
		accepted = inBorder && !(suppressBorder && inOKSolved)
	} else if afterSolved {
		accepted = afterBorder && !(suppressBorder && afterOKSolved)
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
		if inAttempted && inHas {
			value := inBest
			mainScore = &value
		}
		// Балл дорешки храним отдельно и только если он строго больше основного —
		// ячейка показывает «основной (дорешка)», вклад в сумму берёт максимум.
		if afterAttempted && afterHas {
			base := 0
			if mainScore != nil {
				base = *mainScore
			}
			if afterBest > base || mainScore == nil {
				value := afterBest
				practiceScore = &value
			}
		}
	}

	return status, mainScore, practiceScore, upsolved, accepted, true
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
		okSolved:  make(map[string]struct{}),
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
	for key := range src.okSolved {
		if dst.okSolved == nil {
			dst.okSolved = make(map[string]struct{})
		}
		dst.okSolved[key] = struct{}{}
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
