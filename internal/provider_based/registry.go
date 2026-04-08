package providerbased

import (
	"context"
	"sort"
	"strings"
	"sync"

	"standings-edu/internal/domain"
)

type ProviderBuildInput struct {
	Source   *domain.SourceData
	Group    domain.GroupDefinition
	Contest  domain.Contest
	Students []domain.Student
}

type ContestStandingsProvider interface {
	ProviderID() string
	BuildStandings(ctx context.Context, input ProviderBuildInput) (domain.GeneratedContestStandings, error)
}

type ContestProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]ContestStandingsProvider
}

func NewContestProviderRegistry() *ContestProviderRegistry {
	return &ContestProviderRegistry{
		providers: make(map[string]ContestStandingsProvider),
	}
}

func (r *ContestProviderRegistry) Register(provider ContestStandingsProvider) {
	if provider == nil {
		return
	}
	id := normalizeProviderID(provider.ProviderID())
	if id == "" {
		return
	}
	r.mu.Lock()
	r.providers[id] = provider
	r.mu.Unlock()
}

func (r *ContestProviderRegistry) Get(providerID string) (ContestStandingsProvider, bool) {
	id := normalizeProviderID(providerID)
	if id == "" {
		return nil, false
	}
	r.mu.RLock()
	provider, ok := r.providers[id]
	r.mu.RUnlock()
	return provider, ok
}

func (r *ContestProviderRegistry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.providers))
	for id := range r.providers {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func normalizeProviderID(providerID string) string {
	return strings.ToLower(strings.TrimSpace(providerID))
}
