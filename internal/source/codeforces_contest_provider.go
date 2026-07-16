package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"standings-edu/internal/domain"
)

const CodeforcesContestProviderID = "codeforces_contest"

type CodeforcesContestProvider struct {
	client *CodeforcesAPIClient
}

func NewCodeforcesContestProvider(client *CodeforcesAPIClient) *CodeforcesContestProvider {
	return &CodeforcesContestProvider{client: client}
}

func (p *CodeforcesContestProvider) ProviderID() string {
	return CodeforcesContestProviderID
}

// ParseCodeforcesContestID распознаёт ссылку на КОНТЕСТ Codeforces (а не на задачу):
//
//	https://codeforces.com/contest/<id>
//	https://codeforces.com/gym/<id>
//	https://codeforces.com/group/<gid>/contest/<id>
//
// Возвращает (id, true). Ссылка на конкретную задачу (есть сегмент "problem") — (0, false).
func ParseCodeforcesContestID(rawURL string) (int, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, false
	}
	host := strings.ToLower(u.Hostname())
	if host != "codeforces.com" && host != "www.codeforces.com" {
		return 0, false
	}

	segments := make([]string, 0)
	for _, s := range strings.Split(u.Path, "/") {
		if s = strings.TrimSpace(s); s != "" {
			segments = append(segments, s)
		}
	}
	for _, s := range segments {
		if strings.EqualFold(s, "problem") {
			return 0, false
		}
	}
	for i := 0; i+1 < len(segments); i++ {
		if strings.EqualFold(segments[i], "contest") || strings.EqualFold(segments[i], "gym") {
			if id, err := strconv.Atoi(segments[i+1]); err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}

// ParseCodeforcesProblemURL распознаёт ссылку на ОТДЕЛЬНУЮ задачу codeforces и
// возвращает contest_id и индекс задачи (буква/номер). Формы:
//   - /contest/<id>/problem/<idx>
//   - /gym/<id>/problem/<idx>
//   - /problemset/problem/<id>/<idx>
func ParseCodeforcesProblemURL(rawURL string) (contestID int, index string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, "", false
	}
	if host := strings.ToLower(u.Hostname()); host != "codeforces.com" && host != "www.codeforces.com" {
		return 0, "", false
	}
	seg := make([]string, 0)
	for _, s := range strings.Split(u.Path, "/") {
		if s = strings.TrimSpace(s); s != "" {
			seg = append(seg, s)
		}
	}
	// /problemset/problem/<id>/<idx>
	if len(seg) == 4 && strings.EqualFold(seg[0], "problemset") && strings.EqualFold(seg[1], "problem") {
		if id, err := strconv.Atoi(seg[2]); err == nil && id > 0 && seg[3] != "" {
			return id, seg[3], true
		}
		return 0, "", false
	}
	// /contest/<id>/problem/<idx> или /gym/<id>/problem/<idx>
	for i := 0; i+3 < len(seg); i++ {
		if (strings.EqualFold(seg[i], "contest") || strings.EqualFold(seg[i], "gym")) && strings.EqualFold(seg[i+2], "problem") {
			if id, err := strconv.Atoi(seg[i+1]); err == nil && id > 0 && seg[i+3] != "" {
				return id, seg[i+3], true
			}
		}
	}
	return 0, "", false
}

func (p *CodeforcesContestProvider) BuildStandings(ctx context.Context, input ContestProviderInput) (domain.GeneratedContestStandings, error) {
	if p == nil || p.client == nil {
		return domain.GeneratedContestStandings{}, fmt.Errorf("codeforces contest provider client is not configured")
	}

	cfg, err := parseCodeforcesContestProviderConfig(input.Contest.ProviderConfig)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	participants, err := resolveDefaultCodeforcesParticipants(input.Students)
	if err != nil {
		return domain.GeneratedContestStandings{}, err
	}

	handles := make([]string, 0, len(participants))
	for _, participant := range participants {
		handles = append(handles, participant.Handle)
	}

	// Codeforces запрещает фильтрованный contest.standings (handles/from/count/
	// showUnofficial) для обычных контестов не-админам: доступен только полный
	// анонимный contest.standings?contestId=<id>. Поэтому для обычных контестов
	// строим таблицу из contest.status (он работает по handle и анонимно, и
	// учитывает виртуальные/дорешивание при show_unofficial). Для gym-контестов
	// сохраняем contest.standings как основной путь с fallback на contest.status.
	if !isCodeforcesGymContestID(cfg.ContestID) {
		contestStandings, err := p.buildContestStatusFallbackStandings(ctx, input.Contest, cfg, participants)
		if err != nil {
			return domain.GeneratedContestStandings{}, fmt.Errorf("fetch codeforces contest standings via contest.status: %w", err)
		}
		return buildCodeforcesGeneratedStandings(input.Contest, cfg.ContestID, participants, contestStandings), nil
	}

	contestStandings, err := p.client.FetchContestStandings(ctx, cfg.ContestID, handles, cfg.showUnofficialOrDefault())
	if err != nil {
		primaryErr := err

		log.Printf(
			"codeforces contest provider: primary contest.standings failed, fallback to contest.status (contest_id=%d): %v",
			cfg.ContestID,
			primaryErr,
		)

		contestStandings, err = p.buildContestStatusFallbackStandings(ctx, input.Contest, cfg, participants)
		if err != nil {
			fallbackErr := err
			return domain.GeneratedContestStandings{}, fmt.Errorf(
				"fetch codeforces contest standings: primary contest.standings failed: %v; fallback contest.status failed: %w",
				primaryErr,
				fallbackErr,
			)
		}
	}

	return buildCodeforcesGeneratedStandings(input.Contest, cfg.ContestID, participants, contestStandings), nil
}

func isCodeforcesGymContestID(contestID int) bool {
	return contestID >= 100000
}

func (p *CodeforcesContestProvider) buildContestStatusFallbackStandings(
	ctx context.Context,
	contest domain.Contest,
	cfg codeforcesContestProviderConfig,
	participants []codeforcesContestParticipant,
) (CodeforcesContestStandings, error) {
	handles := make([]string, 0, len(participants))
	for _, participant := range participants {
		handles = append(handles, participant.Handle)
	}

	submissions, err := p.client.FetchContestStatusSubmissions(
		ctx,
		cfg.ContestID,
		handles,
		cfg.showUnofficialOrDefault(),
	)
	if err != nil {
		return CodeforcesContestStandings{}, fmt.Errorf("fetch contest.status: %w", err)
	}

	return buildCodeforcesContestStandingsFromStatus(
		contest.ScoreSystem,
		cfg.ContestID,
		cfg.showUnofficialOrDefault(),
		participants,
		submissions,
	), nil
}

type codeforcesContestProviderConfig struct {
	ContestID      int   `json:"contest_id"`
	ShowUnofficial *bool `json:"show_unofficial,omitempty"`
}

type codeforcesContestParticipant struct {
	Handle     string
	StudentID  string
	PublicName string
}

type providerBuiltRow struct {
	rank int
	row  domain.GeneratedRow
}

func (c codeforcesContestProviderConfig) showUnofficialOrDefault() bool {
	if c.ShowUnofficial == nil {
		return true
	}
	return *c.ShowUnofficial
}

func parseCodeforcesContestProviderConfig(raw json.RawMessage) (codeforcesContestProviderConfig, error) {
	var cfg codeforcesContestProviderConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return cfg, fmt.Errorf("provider_config is required for provider=%q", CodeforcesContestProviderID)
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode provider_config: %w", err)
	}

	if cfg.ContestID <= 0 {
		return cfg, fmt.Errorf("provider_config.contest_id must be > 0")
	}

	return cfg, nil
}

func resolveDefaultCodeforcesParticipants(students []domain.Student) ([]codeforcesContestParticipant, error) {
	participants := make([]codeforcesContestParticipant, 0, len(students))
	seen := make(map[string]struct{})
	for _, student := range students {
		for _, account := range student.Accounts {
			if domain.NormalizeSite(account.Site) != "codeforces" {
				continue
			}

			handle := strings.TrimSpace(account.AccountID)
			if handle == "" {
				continue
			}

			key := strings.ToLower(handle)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			participants = append(participants, codeforcesContestParticipant{
				Handle:     handle,
				StudentID:  student.ID,
				PublicName: student.PublicName,
			})
		}
	}

	if len(participants) == 0 {
		return nil, fmt.Errorf("no codeforces participants resolved from group students/accounts")
	}
	return participants, nil
}

func buildCodeforcesGeneratedStandings(
	contest domain.Contest,
	configContestID int,
	participants []codeforcesContestParticipant,
	standings CodeforcesContestStandings,
) domain.GeneratedContestStandings {
	isIOI := contest.ScoreSystem.IsIOI()

	actualContestID := standings.ContestID
	if actualContestID <= 0 {
		actualContestID = configContestID
	}

	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		ScoreSystem: contest.ScoreSystem.Normalized(),
		Subcontests: make([]domain.GeneratedSubcontest, 0, 1),
		Tasks:       make([]domain.GeneratedTask, 0, len(standings.Problems)),
		Rows:        make([]domain.GeneratedRow, 0, len(participants)),
	}

	tasks := make([]domain.GeneratedTask, 0, len(standings.Problems))
	for i, problem := range standings.Problems {
		label := strings.TrimSpace(problem.Index)
		if label == "" {
			label = domain.AlphabetLabel(i)
		}
		taskURL := buildCodeforcesContestProblemURL(actualContestID, problem.Index)
		tasks = append(tasks, domain.GeneratedTask{
			Label:         label,
			URL:           taskURL,
			NormalizedURL: domain.NormalizeTaskURL(taskURL),
			Name:          strings.TrimSpace(problem.Name),
		})
	}

	out.Subcontests = append(out.Subcontests, domain.GeneratedSubcontest{
		Title:     "Результаты",
		TaskCount: len(tasks),
		Tasks:     tasks,
	})
	out.Tasks = append(out.Tasks, tasks...)

	type matchedRow struct {
		rank int
		row  CodeforcesContestRow
	}

	rowByHandle := make(map[string]matchedRow, len(standings.Rows))
	for i := range standings.Rows {
		row := standings.Rows[i]
		rank := row.Rank
		if rank <= 0 {
			rank = i + 1
		}
		for _, handle := range row.Handles {
			key := strings.ToLower(strings.TrimSpace(handle))
			if key == "" {
				continue
			}
			if _, exists := rowByHandle[key]; exists {
				continue
			}
			rowByHandle[key] = matchedRow{rank: rank, row: row}
		}
	}

	builtRows := make([]providerBuiltRow, 0, len(participants))

	for _, participant := range participants {
		match, ok := rowByHandle[strings.ToLower(strings.TrimSpace(participant.Handle))]
		rank := 1_000_000_000
		if ok {
			rank = match.rank
		}

		row := domain.GeneratedRow{
			StudentID:  participant.StudentID,
			PublicName: participant.PublicName,
			Statuses:   make([]string, len(out.Tasks)),
		}
		if ok && match.row.Penalty != nil {
			penalty := *match.row.Penalty
			row.Penalty = &penalty
		}
		for i := range row.Statuses {
			row.Statuses[i] = domain.TaskStatusNone
		}
		if isIOI {
			row.Scores = make([]*int, len(out.Tasks))
		}
		upsolved := make([]bool, len(out.Tasks))
		hasUpsolved := false

		if ok {
			for taskIdx := range out.Tasks {
				status := domain.TaskStatusNone
				score := 0
				attempted := false
				solved := false

				if taskIdx < len(match.row.ProblemResults) {
					problemResult := match.row.ProblemResults[taskIdx]
					score = int(math.Round(problemResult.Points))
					attempted = score > 0 || problemResult.RejectedAttemptCount > 0

					maxPoints := 0
					hasMaxPoints := false
					if taskIdx < len(standings.Problems) && standings.Problems[taskIdx].Points != nil {
						hasMaxPoints = true
						maxPoints = int(math.Round(*standings.Problems[taskIdx].Points))
					}

					if hasMaxPoints && maxPoints > 0 {
						solved = score >= maxPoints
					} else if score > 0 {
						solved = true
					}

					if (solved || attempted) && problemResult.Upsolved {
						upsolved[taskIdx] = true
						hasUpsolved = true
					}
				}

				switch {
				case solved:
					status = domain.TaskStatusSolved
					row.SolvedCount++
				case attempted:
					status = domain.TaskStatusAttempted
				default:
					status = domain.TaskStatusNone
				}
				row.Statuses[taskIdx] = status

				if isIOI && attempted {
					value := score
					row.Scores[taskIdx] = &value
					row.TotalScore += value
				}
			}
		}

		if hasUpsolved {
			row.Upsolved = upsolved
		}

		builtRows = append(builtRows, providerBuiltRow{rank: rank, row: row})
	}

	sort.SliceStable(builtRows, func(i, j int) bool {
		if builtRows[i].rank != builtRows[j].rank {
			return builtRows[i].rank < builtRows[j].rank
		}
		return strings.ToLower(builtRows[i].row.PublicName) < strings.ToLower(builtRows[j].row.PublicName)
	})

	assignProviderPlaces(builtRows)

	for _, item := range builtRows {
		out.Rows = append(out.Rows, item.row)
	}

	return out
}

func buildCodeforcesContestProblemURL(contestID int, index string) string {
	idx := strings.TrimSpace(index)
	if contestID <= 0 || idx == "" {
		return ""
	}

	if contestID >= 100000 {
		return fmt.Sprintf("https://codeforces.com/gym/%d/problem/%s", contestID, url.PathEscape(idx))
	}
	return fmt.Sprintf("https://codeforces.com/contest/%d/problem/%s", contestID, url.PathEscape(idx))
}

// assignProviderPlaces проставляет места группе участников по уже отсортированным
// строкам. Места — относительные внутри группы (1..k): таблицу мы строим сами,
// поэтому и нумерацию ведём свою, а не глобальный ранг контеста. Одинаковый ранг
// получает общий диапазон ("3-5"); участники без места (не найдены в контесте)
// получают пустую строку.
func assignProviderPlaces(rows []providerBuiltRow) {
	const missingRank = 1_000_000_000
	pos := 0
	i := 0
	for i < len(rows) {
		if rows[i].rank >= missingRank {
			rows[i].row.Place = ""
			i++
			continue
		}

		rank := rows[i].rank
		j := i + 1
		for j < len(rows) && rows[j].rank < missingRank && rows[j].rank == rank {
			j++
		}

		start := pos + 1
		end := pos + (j - i)
		if j-i == 1 {
			rows[i].row.Place = fmt.Sprintf("%d", start)
		} else {
			place := fmt.Sprintf("%d-%d", start, end)
			for k := i; k < j; k++ {
				rows[k].row.Place = place
			}
		}
		pos = end
		i = j
	}
}

type codeforcesFallbackProblemKey struct {
	contestID int
	index     string
}

type codeforcesFallbackProblemMeta struct {
	contestID      int
	index          string
	name           string
	points         *float64
	hasObservedMax bool
	observedMax    float64
}

type codeforcesFallbackSubmissionEvent struct {
	id                  int
	relativeTimeSeconds int
	verdict             string
	points              *float64
	// upsolve — посылка из дорешки (после контеста): participantType=PRACTICE
	// или нереалистично большое relativeTimeSeconds. Такие посылки не идут в
	// зачёт места/штрафа, но отображаются (в скобках).
	upsolve bool
}

type codeforcesFallbackParticipantAggregate struct {
	handles         []string
	eventsByProblem map[codeforcesFallbackProblemKey][]codeforcesFallbackSubmissionEvent
}

type codeforcesFallbackBuiltRow struct {
	row        CodeforcesContestRow
	solved     int
	totalScore float64
	penalty    int
	sortKey    string
}

type codeforcesFallbackProblemStats struct {
	// Отображение (контест + дорешка).
	points           float64
	rejectedAttempts int
	solved           bool
	upsolved         bool // решено/попытано только в дорешке

	// Зачёт места/штрафа (только во время контеста).
	contestSolved bool
	contestPoints float64
	penalty       int
}

type codeforcesEventSummary struct {
	solved                 bool
	bestPoints             float64
	hasPoints              bool
	rejectedBeforeAccepted int
	acceptedTimeSeconds    int
	count                  int
}

var codeforcesIndexTokenRe = regexp.MustCompile(`[0-9]+|[^0-9]+`)

const codeforcesFallbackFloatEpsilon = 1e-9

func buildCodeforcesContestStandingsFromStatus(
	scoreSystem domain.ScoreSystem,
	configContestID int,
	showUnofficial bool,
	participants []codeforcesContestParticipant,
	submissions []codeforcesContestStatusSubmission,
) CodeforcesContestStandings {
	out := CodeforcesContestStandings{
		ContestID: configContestID,
		Problems:  make([]CodeforcesContestProblem, 0),
		Rows:      make([]CodeforcesContestRow, 0),
	}
	isIOI := scoreSystem.IsIOI()

	targetHandles := make(map[string]string, len(participants))
	for _, participant := range participants {
		key := strings.ToLower(strings.TrimSpace(participant.Handle))
		if key == "" {
			continue
		}
		targetHandles[key] = participant.Handle
	}

	problemMetaByKey := make(map[codeforcesFallbackProblemKey]codeforcesFallbackProblemMeta)
	aggregatesByParty := make(map[string]*codeforcesFallbackParticipantAggregate)

	for _, submission := range submissions {
		if !showUnofficial && !isCodeforcesStatusOfficialParticipant(submission.Author.ParticipantType) {
			continue
		}

		matchedHandles := matchCodeforcesAuthorHandles(submission.Author.Members, targetHandles)
		if len(matchedHandles) == 0 {
			continue
		}

		problemIndex := strings.TrimSpace(submission.Problem.Index)
		if problemIndex == "" {
			continue
		}

		problemContestID := submission.Problem.ContestID
		if problemContestID <= 0 {
			problemContestID = configContestID
		}
		if problemContestID <= 0 {
			continue
		}

		problemKey := codeforcesFallbackProblemKey{
			contestID: problemContestID,
			index:     problemIndex,
		}
		meta := problemMetaByKey[problemKey]
		meta.contestID = problemContestID
		meta.index = problemIndex
		if meta.name == "" {
			meta.name = strings.TrimSpace(submission.Problem.Name)
		}
		if submission.Points != nil {
			if meta.points == nil && submission.Problem.Points != nil {
				value := *submission.Problem.Points
				meta.points = &value
			}
			if !meta.hasObservedMax || *submission.Points > meta.observedMax {
				meta.observedMax = *submission.Points
				meta.hasObservedMax = true
			}
		}
		problemMetaByKey[problemKey] = meta

		partyKey := buildCodeforcesPartyKey(matchedHandles)
		aggregate, ok := aggregatesByParty[partyKey]
		if !ok {
			aggregate = &codeforcesFallbackParticipantAggregate{
				handles:         append([]string(nil), matchedHandles...),
				eventsByProblem: make(map[codeforcesFallbackProblemKey][]codeforcesFallbackSubmissionEvent),
			}
			aggregatesByParty[partyKey] = aggregate
		}

		event := codeforcesFallbackSubmissionEvent{
			id:                  submission.ID,
			relativeTimeSeconds: submission.RelativeTimeSeconds,
			verdict:             strings.TrimSpace(submission.Verdict),
			upsolve:             isCodeforcesUpsolveSubmission(submission),
		}
		if submission.Points != nil {
			value := *submission.Points
			event.points = &value
		}
		aggregate.eventsByProblem[problemKey] = append(aggregate.eventsByProblem[problemKey], event)
	}

	problemOrder := make([]codeforcesFallbackProblemKey, 0, len(problemMetaByKey))
	for key := range problemMetaByKey {
		problemOrder = append(problemOrder, key)
	}
	sort.Slice(problemOrder, func(i, j int) bool {
		if problemOrder[i].contestID != problemOrder[j].contestID {
			return problemOrder[i].contestID < problemOrder[j].contestID
		}
		return compareCodeforcesProblemIndex(problemOrder[i].index, problemOrder[j].index) < 0
	})

	out.Problems = make([]CodeforcesContestProblem, 0, len(problemOrder))
	problemIndexByKey := make(map[codeforcesFallbackProblemKey]int, len(problemOrder))
	for i, key := range problemOrder {
		meta := problemMetaByKey[key]
		points := meta.points
		if points == nil && isIOI && meta.hasObservedMax {
			value := meta.observedMax
			points = &value
		}

		out.Problems = append(out.Problems, CodeforcesContestProblem{
			Index:  meta.index,
			Name:   meta.name,
			Points: points,
		})
		problemIndexByKey[key] = i
	}

	builtRows := make([]codeforcesFallbackBuiltRow, 0, len(aggregatesByParty))
	for _, aggregate := range aggregatesByParty {
		row := CodeforcesContestRow{
			Handles:        append([]string(nil), aggregate.handles...),
			ProblemResults: make([]CodeforcesContestProblemResult, len(problemOrder)),
		}
		built := codeforcesFallbackBuiltRow{
			row:     row,
			sortKey: strings.ToLower(strings.Join(aggregate.handles, ";")),
		}

		for problemKey, events := range aggregate.eventsByProblem {
			taskIdx, ok := problemIndexByKey[problemKey]
			if !ok {
				continue
			}

			stats := aggregateCodeforcesFallbackProblemStats(events, isIOI)
			built.row.ProblemResults[taskIdx] = CodeforcesContestProblemResult{
				Points:               stats.points,
				RejectedAttemptCount: stats.rejectedAttempts,
				Upsolved:             stats.upsolved,
			}
			// Место/штраф считаем только по результатам во время контеста;
			// дорешка отображается, но в зачёт не идёт.
			if stats.contestSolved {
				built.solved++
			}
			if isIOI {
				built.totalScore += stats.contestPoints
			} else {
				built.penalty += stats.penalty
			}
		}

		if !isIOI {
			penalty := built.penalty
			built.row.Penalty = &penalty
		}
		builtRows = append(builtRows, built)
	}

	sort.SliceStable(builtRows, func(i, j int) bool {
		if isIOI {
			scoreDelta := builtRows[i].totalScore - builtRows[j].totalScore
			if math.Abs(scoreDelta) > codeforcesFallbackFloatEpsilon {
				return builtRows[i].totalScore > builtRows[j].totalScore
			}
			if builtRows[i].solved != builtRows[j].solved {
				return builtRows[i].solved > builtRows[j].solved
			}
		} else {
			if builtRows[i].solved != builtRows[j].solved {
				return builtRows[i].solved > builtRows[j].solved
			}
			if builtRows[i].penalty != builtRows[j].penalty {
				return builtRows[i].penalty < builtRows[j].penalty
			}
		}
		return builtRows[i].sortKey < builtRows[j].sortKey
	})

	rank := 0
	for i := range builtRows {
		if i == 0 {
			rank = 1
		} else if !sameCodeforcesFallbackRank(builtRows[i-1], builtRows[i], isIOI) {
			rank = i + 1
		}

		builtRows[i].row.Rank = rank
		out.Rows = append(out.Rows, builtRows[i].row)
	}

	return out
}

func matchCodeforcesAuthorHandles(members []codeforcesContestMember, targetHandles map[string]string) []string {
	out := make([]string, 0, len(members))
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		rawHandle := strings.TrimSpace(member.Handle)
		if rawHandle == "" {
			continue
		}
		key := strings.ToLower(rawHandle)
		canonical, ok := targetHandles[key]
		if !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, canonical)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

func buildCodeforcesPartyKey(handles []string) string {
	normalized := make([]string, 0, len(handles))
	for _, handle := range handles {
		key := strings.ToLower(strings.TrimSpace(handle))
		if key == "" {
			continue
		}
		normalized = append(normalized, key)
	}
	sort.Strings(normalized)
	return strings.Join(normalized, ";")
}

// codeforcesUpsolveTimeThreshold — посылки с relativeTimeSeconds не меньше этого
// значения считаются дорешкой (Codeforces отдаёт 2147483647 для practice).
const codeforcesUpsolveTimeThreshold = 1 << 30

func isCodeforcesUpsolveSubmission(submission codeforcesContestStatusSubmission) bool {
	if strings.EqualFold(strings.TrimSpace(submission.Author.ParticipantType), "PRACTICE") {
		return true
	}
	return submission.RelativeTimeSeconds >= codeforcesUpsolveTimeThreshold
}

func summarizeCodeforcesEvents(events []codeforcesFallbackSubmissionEvent) codeforcesEventSummary {
	summary := codeforcesEventSummary{count: len(events)}
	if len(events) == 0 {
		return summary
	}

	sort.SliceStable(events, func(i, j int) bool {
		if events[i].relativeTimeSeconds != events[j].relativeTimeSeconds {
			return events[i].relativeTimeSeconds < events[j].relativeTimeSeconds
		}
		return events[i].id < events[j].id
	})

	for _, event := range events {
		if event.points != nil {
			if !summary.hasPoints || *event.points > summary.bestPoints {
				summary.bestPoints = *event.points
				summary.hasPoints = true
			}
		}
	}

	rejectedBeforeAccepted := 0
	for _, event := range events {
		if strings.EqualFold(event.verdict, "OK") {
			summary.solved = true
			acceptedTime := event.relativeTimeSeconds
			if acceptedTime < 0 {
				acceptedTime = 0
			}
			summary.acceptedTimeSeconds = acceptedTime
			summary.rejectedBeforeAccepted = rejectedBeforeAccepted
			return summary
		}
		rejectedBeforeAccepted++
	}
	summary.rejectedBeforeAccepted = rejectedBeforeAccepted
	return summary
}

func aggregateCodeforcesFallbackProblemStats(events []codeforcesFallbackSubmissionEvent, isIOI bool) codeforcesFallbackProblemStats {
	if len(events) == 0 {
		return codeforcesFallbackProblemStats{}
	}

	contestEvents := make([]codeforcesFallbackSubmissionEvent, 0, len(events))
	upsolveEvents := make([]codeforcesFallbackSubmissionEvent, 0, len(events))
	for _, event := range events {
		if event.upsolve {
			upsolveEvents = append(upsolveEvents, event)
		} else {
			contestEvents = append(contestEvents, event)
		}
	}

	contest := summarizeCodeforcesEvents(contestEvents)
	upsolve := summarizeCodeforcesEvents(upsolveEvents)

	stats := codeforcesFallbackProblemStats{}

	// Зачёт места/штрафа — только во время контеста.
	stats.contestSolved = contest.solved
	if isIOI {
		if contest.hasPoints {
			stats.contestPoints = contest.bestPoints
		} else if contest.solved {
			stats.contestPoints = 1
		}
	} else if contest.solved {
		stats.penalty = contest.acceptedTimeSeconds/60 + contest.rejectedBeforeAccepted*20
	}

	// Отображение — контест плюс дорешка.
	displaySolved := contest.solved || upsolve.solved
	stats.solved = displaySolved

	if isIOI {
		best := 0.0
		has := false
		if contest.hasPoints {
			best, has = contest.bestPoints, true
		}
		if upsolve.hasPoints && (!has || upsolve.bestPoints > best) {
			best, has = upsolve.bestPoints, true
		}
		if !has && displaySolved {
			best, has = 1, true
		}
		stats.points = best
	} else if displaySolved {
		stats.points = 1
	}

	switch {
	case contest.solved:
		stats.upsolved = false
	case upsolve.solved:
		stats.upsolved = true
	case contest.count > 0:
		stats.upsolved = false
	case upsolve.count > 0:
		stats.upsolved = true
	}

	if displaySolved {
		stats.rejectedAttempts = contest.rejectedBeforeAccepted
	} else {
		stats.rejectedAttempts = contest.count + upsolve.count
	}

	return stats
}

func sameCodeforcesFallbackRank(prev codeforcesFallbackBuiltRow, curr codeforcesFallbackBuiltRow, isIOI bool) bool {
	if isIOI {
		return math.Abs(prev.totalScore-curr.totalScore) <= codeforcesFallbackFloatEpsilon && prev.solved == curr.solved
	}
	return prev.solved == curr.solved && prev.penalty == curr.penalty
}

func compareCodeforcesProblemIndex(left string, right string) int {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)

	leftTokens := codeforcesIndexTokenRe.FindAllString(left, -1)
	rightTokens := codeforcesIndexTokenRe.FindAllString(right, -1)
	limit := len(leftTokens)
	if len(rightTokens) < limit {
		limit = len(rightTokens)
	}

	for i := 0; i < limit; i++ {
		lToken := leftTokens[i]
		rToken := rightTokens[i]

		lNum, lErr := strconv.Atoi(lToken)
		rNum, rErr := strconv.Atoi(rToken)
		if lErr == nil && rErr == nil {
			if lNum != rNum {
				if lNum < rNum {
					return -1
				}
				return 1
			}
			continue
		}

		lNorm := strings.ToLower(lToken)
		rNorm := strings.ToLower(rToken)
		if lNorm == rNorm {
			continue
		}
		if lNorm < rNorm {
			return -1
		}
		return 1
	}

	if len(leftTokens) != len(rightTokens) {
		if len(leftTokens) < len(rightTokens) {
			return -1
		}
		return 1
	}

	lNorm := strings.ToLower(left)
	rNorm := strings.ToLower(right)
	if lNorm == rNorm {
		return 0
	}
	if lNorm < rNorm {
		return -1
	}
	return 1
}
