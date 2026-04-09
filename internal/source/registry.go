package source

import (
	"context"
	"sort"
	"strings"
	"sync"

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
	mu        sync.RWMutex
	sites     map[string]SiteClient
	providers map[string]ContestProvider
}

func NewRegistry() *Registry {
	return &Registry{
		sites:     make(map[string]SiteClient),
		providers: make(map[string]ContestProvider),
	}
}

func (r *Registry) RegisterSite(site string, client SiteClient) {
	site = normalizeKey(site)
	if site == "" || client == nil {
		return
	}
	r.mu.Lock()
	r.sites[site] = client
	r.mu.Unlock()
}

func (r *Registry) Site(site string) (SiteClient, bool) {
	site = normalizeKey(site)
	r.mu.RLock()
	client, ok := r.sites[site]
	r.mu.RUnlock()
	return client, ok
}

func (r *Registry) ResolveSiteByTaskURL(taskURL string) (string, SiteClient, bool) {
	taskURL = strings.TrimSpace(taskURL)
	if taskURL == "" {
		return "", nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for site, client := range r.sites {
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
	r.mu.Lock()
	r.providers[id] = provider
	r.mu.Unlock()
}

func (r *Registry) Provider(providerID string) (ContestProvider, bool) {
	id := normalizeKey(providerID)
	if id == "" {
		return nil, false
	}
	r.mu.RLock()
	provider, ok := r.providers[id]
	r.mu.RUnlock()
	return provider, ok
}

func (r *Registry) SiteNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.sites))
	for site := range r.sites {
		out = append(out, site)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) ProviderIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.providers))
	for id := range r.providers {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func normalizeKey(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}
