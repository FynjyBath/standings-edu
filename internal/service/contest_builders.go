package service

import (
	"context"
	"fmt"
	"strings"

	"standings-edu/internal/domain"
	providerbased "standings-edu/internal/provider_based"
)

type contestBuildInput struct {
	source          *domain.SourceData
	group           domain.GroupDefinition
	contest         domain.Contest
	students        []domain.Student
	statusByStudent map[string]*accountStatuses
}

type contestBuilder interface {
	Name() string
	Supports(contest domain.Contest) bool
	RequiredSites(sb *StandingsBuilder, contest domain.Contest) map[string]struct{}
	Build(ctx context.Context, sb *StandingsBuilder, input contestBuildInput) (domain.GeneratedContestStandings, error)
}

type taskContestBuilder struct{}

func (b *taskContestBuilder) Name() string {
	return "task_contest_builder"
}

func (b *taskContestBuilder) Supports(contest domain.Contest) bool {
	return contest.TypeOrDefault() == domain.ContestTypeTasks
}

func (b *taskContestBuilder) RequiredSites(sb *StandingsBuilder, contest domain.Contest) map[string]struct{} {
	out := make(map[string]struct{})
	if sb == nil || sb.registry == nil {
		return out
	}

	for _, sc := range contest.Subcontests {
		for _, taskURL := range sc.Tasks {
			normalized := domain.NormalizeTaskURL(taskURL)
			site, _, ok := sb.registry.ResolveByTaskURL(normalized)
			if !ok || site == "" {
				continue
			}
			out[normalizeSite(site)] = struct{}{}
		}
	}
	return out
}

func (b *taskContestBuilder) Build(_ context.Context, sb *StandingsBuilder, input contestBuildInput) (domain.GeneratedContestStandings, error) {
	if sb == nil {
		return domain.GeneratedContestStandings{}, fmt.Errorf("standings builder is nil")
	}
	return sb.buildContestStandings(input.contest, input.students, input.statusByStudent), nil
}

type providerContestBuilder struct {
	providers *providerbased.ContestProviderRegistry
}

func newProviderContestBuilder(providers *providerbased.ContestProviderRegistry) *providerContestBuilder {
	if providers == nil {
		providers = providerbased.NewContestProviderRegistry()
	}
	return &providerContestBuilder{providers: providers}
}

func (b *providerContestBuilder) Name() string {
	return "provider_contest_builder"
}

func (b *providerContestBuilder) Supports(contest domain.Contest) bool {
	return contest.TypeOrDefault() == domain.ContestTypeProvider
}

func (b *providerContestBuilder) RequiredSites(_ *StandingsBuilder, _ domain.Contest) map[string]struct{} {
	return map[string]struct{}{}
}

func (b *providerContestBuilder) Build(ctx context.Context, _ *StandingsBuilder, input contestBuildInput) (domain.GeneratedContestStandings, error) {
	if b.providers == nil {
		return domain.GeneratedContestStandings{}, fmt.Errorf("providers registry is not configured")
	}

	providerID := strings.TrimSpace(input.contest.Provider)
	if providerID == "" {
		return domain.GeneratedContestStandings{}, fmt.Errorf("provider contest requires non-empty provider")
	}

	provider, ok := b.providers.Get(providerID)
	if !ok {
		return domain.GeneratedContestStandings{}, fmt.Errorf("unknown provider %q", providerID)
	}

	return provider.BuildStandings(ctx, providerbased.ProviderBuildInput{
		Source:   input.source,
		Group:    input.group,
		Contest:  input.contest,
		Students: input.students,
	})
}
