package providerbased

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/tasks_based"
)

const CodeforcesContestProviderID = "codeforces_contest"

type codeforcesContestStandingsClient interface {
	FetchContestStandings(ctx context.Context, contestID int, handles []string, showUnofficial bool) (tasksbased.CodeforcesContestStandings, error)
}

type CodeforcesContestProvider struct {
	client codeforcesContestStandingsClient
}

func NewCodeforcesContestProvider(client codeforcesContestStandingsClient) *CodeforcesContestProvider {
	return &CodeforcesContestProvider{client: client}
}

func (p *CodeforcesContestProvider) ProviderID() string {
	return CodeforcesContestProviderID
}

func (p *CodeforcesContestProvider) BuildStandings(ctx context.Context, input ProviderBuildInput) (domain.GeneratedContestStandings, error) {
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
		return domain.GeneratedContestStandings{}, fmt.Errorf("fetch codeforces contest standings: %w", err)
	}

	return buildCodeforcesGeneratedStandings(input.Contest, cfg.ContestID, participants, contestStandings), nil
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
			if normalizeSite(account.Site) != "codeforces" {
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
	standings tasksbased.CodeforcesContestStandings,
) domain.GeneratedContestStandings {
	actualContestID := standings.ContestID
	if actualContestID <= 0 {
		actualContestID = configContestID
	}

	out := domain.GeneratedContestStandings{
		ID:          contest.ID,
		Title:       contest.Title,
		Olympiad:    contest.Olympiad,
		Subcontests: make([]domain.GeneratedSubcontest, 0, 1),
		Tasks:       make([]domain.GeneratedTask, 0, len(standings.Problems)),
		Rows:        make([]domain.GeneratedRow, 0, len(participants)),
	}

	tasks := make([]domain.GeneratedTask, 0, len(standings.Problems))
	for i, problem := range standings.Problems {
		label := strings.TrimSpace(problem.Index)
		if label == "" {
			label = alphabetLabel(i)
		}
		taskURL := buildCodeforcesContestProblemURL(actualContestID, problem.Index)
		tasks = append(tasks, domain.GeneratedTask{
			Label:         label,
			URL:           taskURL,
			NormalizedURL: domain.NormalizeTaskURL(taskURL),
		})
	}

	subcontestTitle := "Codeforces Contest"
	if strings.TrimSpace(standings.ContestName) != "" {
		subcontestTitle = strings.TrimSpace(standings.ContestName)
	}
	out.Subcontests = append(out.Subcontests, domain.GeneratedSubcontest{
		Title:     subcontestTitle,
		TaskCount: len(tasks),
		Tasks:     tasks,
	})
	out.Tasks = append(out.Tasks, tasks...)

	type matchedRow struct {
		rank int
		row  tasksbased.CodeforcesContestRow
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
		if contest.Olympiad {
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

				if contest.Olympiad && attempted {
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
