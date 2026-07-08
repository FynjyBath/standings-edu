package source

import (
	"context"
	"strings"
	"time"

	"standings-edu/internal/domain"
)

type TaskResult struct {
	TaskURL   string
	Attempted bool
	Solved    bool
	Score     *int
	// Accepted — задача решена статусом «зачтено» (ejudge/informatics
	// RUN_ACCEPTED), а полного OK не было. Такие ячейки помечаются в таблице.
	Accepted bool `json:"accepted,omitempty"`
	// Timed — посылки с временем (для сайтов, которые его отдают: codeforces,
	// informatics). Пусто — сайт время не отдаёт, фильтрация по окну контеста
	// к этой задаче не применяется. Хранится в кэше.
	Timed []TimedSubmission `json:"Timed,omitempty"`
}

// TimedSubmission — одна посылка с временем (для фильтрации по окну контеста).
type TimedSubmission struct {
	At     time.Time `json:"at"`
	Solved bool      `json:"solved,omitempty"`
	Score  *int      `json:"score,omitempty"`
	// Accepted — посылка «зачтена» (RUN_ACCEPTED), а не полный OK.
	Accepted bool `json:"accepted,omitempty"`
}

func cloneTimedSubmissions(in []TimedSubmission) []TimedSubmission {
	if len(in) == 0 {
		return nil
	}
	out := make([]TimedSubmission, 0, len(in))
	for _, sub := range in {
		copySub := sub
		if sub.Score != nil {
			score := *sub.Score
			copySub.Score = &score
		}
		out = append(out, copySub)
	}
	return out
}

type SiteClient interface {
	FetchUserResults(ctx context.Context, accountID string) ([]TaskResult, error)
	SupportsTaskScores() bool
	MatchTaskURL(taskURL string) bool
}

// StatementExpander реализуют сайт-клиенты, умеющие разворачивать ссылку на
// сборник задач в список отдельных задач (informatics). Билд обнаруживает
// возможность через type-assert клиента сайта — расширять базовый SiteClient не
// нужно.
type StatementExpander interface {
	FetchStatementProblems(ctx context.Context, statementID int) ([]InformaticsStatementProblem, error)
}

// TaskURLObserver реализуют клиенты, которым нужно заранее знать ссылки задач,
// чтобы решить, что скачивать (ejudge: из ссылок берутся contest_id для забора
// прогонов). Билд сообщает каждую ссылку задачи до сбора статусов учеников.
type TaskURLObserver interface {
	ObserveTaskURL(taskURL string)
}

// EjudgeContestExpander реализуют клиенты, умеющие разворачивать ссылку на контест
// ejudge в список его задач.
type EjudgeContestExpander interface {
	FetchContestProblems(ctx context.Context, contestID int) ([]EjudgeProblem, error)
}

type ContestProviderInput struct {
	Source   *domain.SourceData
	Group    domain.GroupDefinition
	Contest  domain.Contest
	Students []domain.Student
}

type ContestProvider interface {
	ProviderID() string
	BuildStandings(ctx context.Context, input ContestProviderInput) (domain.GeneratedContestStandings, error)
}

type Registry struct {
	sites        map[string]SiteClient
	sitePriority []string
	providers    map[string]ContestProvider
}

func NewRegistry() *Registry {
	return &Registry{
		sites:        make(map[string]SiteClient),
		sitePriority: make([]string, 0),
		providers:    make(map[string]ContestProvider),
	}
}

func (r *Registry) RegisterSite(site string, client SiteClient) {
	site = normalizeKey(site)
	if site == "" || client == nil {
		return
	}
	if _, exists := r.sites[site]; !exists {
		r.sitePriority = append(r.sitePriority, site)
	}
	r.sites[site] = client
}

func (r *Registry) Site(site string) (SiteClient, bool) {
	site = normalizeKey(site)
	client, ok := r.sites[site]
	return client, ok
}

func (r *Registry) ResolveSiteByTaskURL(taskURL string) (string, SiteClient, bool) {
	taskURL = strings.TrimSpace(taskURL)
	if taskURL == "" {
		return "", nil, false
	}

	for _, site := range r.sitePriority {
		client := r.sites[site]
		if client == nil {
			continue
		}
		if client.MatchTaskURL(taskURL) {
			return site, client, true
		}
	}
	return "", nil, false
}

func (r *Registry) RegisterProvider(provider ContestProvider) {
	if provider == nil {
		return
	}
	id := normalizeKey(provider.ProviderID())
	if id == "" {
		return
	}
	r.providers[id] = provider
}

func (r *Registry) Provider(providerID string) (ContestProvider, bool) {
	id := normalizeKey(providerID)
	if id == "" {
		return nil, false
	}
	provider, ok := r.providers[id]
	return provider, ok
}

func normalizeKey(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}
