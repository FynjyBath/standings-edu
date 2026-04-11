package source

import (
	"context"
	"strings"

	"standings-edu/internal/domain"
)

type TaskResult struct {
	TaskURL   string
	Attempted bool
	Solved    bool
	Score     *int
}

type SiteClient interface {
	FetchUserResults(ctx context.Context, accountID string) ([]TaskResult, error)
	SupportsTaskScores() bool
	MatchTaskURL(taskURL string) bool
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
