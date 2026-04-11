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

	contestStandings, err := p.client.FetchContestStandings(ctx, cfg.ContestID, handles, cfg.showUnofficialOrDefault())
	if err != nil {
		primaryErr := err
		if !isCodeforcesRetriableError(primaryErr) {
			return domain.GeneratedContestStandings{}, fmt.Errorf("fetch codeforces contest standings: %w", primaryErr)
		}

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

func assignProviderPlaces(rows []providerBuiltRow) {
	const missingRank = 1_000_000_000
	i := 0
	for i < len(rows) {
		if rows[i].rank >= missingRank {
			rows[i].row.Place = ""
			i++
			continue
		}

		rank := rows[i].rank
		j := i + 1
		for j < len(rows) && rows[j].rank == rank {
			j++
		}

		if j-i == 1 {
			rows[i].row.Place = fmt.Sprintf("%d", rank)
		} else {
			endRank := rank + (j - i) - 1
			place := fmt.Sprintf("%d-%d", rank, endRank)
			for k := i; k < j; k++ {
				rows[k].row.Place = place
			}
		}
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
	points           float64
	rejectedAttempts int
	solved           bool
	penalty          int
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
		if meta.points == nil && submission.Problem.Points != nil {
			value := *submission.Problem.Points
			meta.points = &value
		}
		if submission.Points != nil {
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
			}
			if stats.solved {
				built.solved++
			}
			if isIOI {
				built.totalScore += stats.points
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

func aggregateCodeforcesFallbackProblemStats(events []codeforcesFallbackSubmissionEvent, isIOI bool) codeforcesFallbackProblemStats {
	if len(events) == 0 {
		return codeforcesFallbackProblemStats{}
	}

	sort.SliceStable(events, func(i, j int) bool {
		if events[i].relativeTimeSeconds != events[j].relativeTimeSeconds {
			return events[i].relativeTimeSeconds < events[j].relativeTimeSeconds
		}
		return events[i].id < events[j].id
	})

	if isIOI {
		bestPoints := 0.0
		hasPoints := false
		solved := false
		for _, event := range events {
			if strings.EqualFold(event.verdict, "OK") {
				solved = true
			}
			if event.points != nil {
				if !hasPoints || *event.points > bestPoints {
					bestPoints = *event.points
					hasPoints = true
				}
			}
		}
		if !hasPoints && solved {
			bestPoints = 100
			hasPoints = true
		}

		rejectedAttempts := 0
		if !hasPoints {
			rejectedAttempts = len(events)
		}

		return codeforcesFallbackProblemStats{
			points:           bestPoints,
			rejectedAttempts: rejectedAttempts,
			solved:           solved,
		}
	}

	rejectedBeforeAccepted := 0
	solved := false
	acceptedTimeSeconds := 0
	for _, event := range events {
		if solved {
			continue
		}
		if strings.EqualFold(event.verdict, "OK") {
			solved = true
			acceptedTimeSeconds = event.relativeTimeSeconds
			if acceptedTimeSeconds < 0 {
				acceptedTimeSeconds = 0
			}
			continue
		}
		rejectedBeforeAccepted++
	}

	points := 0.0
	penalty := 0
	if solved {
		points = 1
		penalty = acceptedTimeSeconds/60 + rejectedBeforeAccepted*20
	}

	if !solved && rejectedBeforeAccepted == 0 {
		rejectedBeforeAccepted = len(events)
	}

	return codeforcesFallbackProblemStats{
		points:           points,
		rejectedAttempts: rejectedBeforeAccepted,
		solved:           solved,
		penalty:          penalty,
	}
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
