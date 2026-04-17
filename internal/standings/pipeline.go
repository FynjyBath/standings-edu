package standings

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
)

type Pipeline struct {
	loader          *storage.SourceLoader
	writer          *storage.GeneratedWriter
	generatedLoader *storage.GeneratedLoader
	builder         *Builder
	logger          *log.Logger
}

func NewPipeline(loader *storage.SourceLoader, writer *storage.GeneratedWriter, builder *Builder, logger *log.Logger) *Pipeline {
	if logger == nil {
		logger = log.Default()
	}
	return &Pipeline{
		loader:          loader,
		writer:          writer,
		generatedLoader: storage.NewGeneratedLoader(writer.OutDir),
		builder:         builder,
		logger:          logger,
	}
}

func (p *Pipeline) Run(ctx context.Context, onlyGroup string) error {
	data, err := p.loader.Load()
	if err != nil {
		return fmt.Errorf("load source data: %w", err)
	}

	selectedGroups := selectGroups(data.Groups, onlyGroup)
	if onlyGroup != "" && len(selectedGroups) == 0 {
		return fmt.Errorf("group %q not found", onlyGroup)
	}

	groupsToUpdate := filterGroupsToUpdate(selectedGroups)
	if len(groupsToUpdate) == 0 {
		p.logger.Printf("INFO no groups with update=true selected; nothing to update")
		return nil
	}

	buildGroups := selectGroupsWithUpdatableContests(groupsToUpdate)
	if len(buildGroups) == 0 {
		p.logger.Printf("INFO no contests with update=true in selected groups; nothing to update")
		return nil
	}

	standingsByGroup, err := p.builder.BuildGroupsStandings(ctx, data, buildGroups)
	if err != nil {
		return fmt.Errorf("build standings: %w", err)
	}

	metas := make([]domain.GeneratedGroupMeta, 0, len(selectedGroups))
	for _, group := range selectedGroups {
		metas = append(metas, domain.GeneratedGroupMeta{Slug: group.Slug, Title: group.Title})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	if err := p.writer.WriteGroups(metas); err != nil {
		return fmt.Errorf("write groups list: %w", err)
	}

	fullGroupBySlug := mapGroupsBySlug(groupsToUpdate)

	generatedCount := 0
	for _, group := range buildGroups {
		p.logger.Printf("INFO generating standings for group=%s", group.Slug)

		updatedStandings, ok := standingsByGroup[group.Slug]
		if !ok {
			p.logger.Printf("ERROR group=%s build result not found", group.Slug)
			continue
		}

		fullGroup, ok := fullGroupBySlug[group.Slug]
		if !ok {
			p.logger.Printf("ERROR group=%s source group not found", group.Slug)
			continue
		}
		mergedStandings, ok := p.mergeWithNonUpdatedContests(fullGroup, updatedStandings)
		if !ok {
			p.logger.Printf("ERROR group=%s merge failed; skip writing to avoid data loss", group.Slug)
			continue
		}

		if err := p.writer.WriteGroupStandings(mergedStandings); err != nil {
			p.logger.Printf("ERROR group=%s write standings failed: %v", group.Slug, err)
			continue
		}

		generatedCount++
		p.logger.Printf("INFO group=%s generated", group.Slug)
	}

	if generatedCount == 0 {
		return fmt.Errorf("no groups generated successfully")
	}

	p.logger.Printf("INFO generation complete: updated %d/%d selected groups", generatedCount, len(buildGroups))
	return nil
}

func (p *Pipeline) mergeWithNonUpdatedContests(group domain.GroupDefinition, updated domain.GeneratedGroupStandings) (domain.GeneratedGroupStandings, bool) {
	hasNonUpdatedContests := false
	for _, contest := range group.Contests {
		if !contest.Update {
			hasNonUpdatedContests = true
			break
		}
	}

	existing := domain.GeneratedGroupStandings{}
	hasExisting := false
	existingLoaded, loadErr := p.generatedLoader.LoadGroupStandings(group.Slug)
	if loadErr == nil {
		existing = existingLoaded
		hasExisting = true
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		p.logger.Printf("WARN group=%s load existing standings failed: %v", group.Slug, loadErr)
	}

	if hasNonUpdatedContests && !hasExisting {
		if errors.Is(loadErr, os.ErrNotExist) {
			p.logger.Printf("WARN group=%s has contests with update=false but previous standings are missing", group.Slug)
		}
		return domain.GeneratedGroupStandings{}, false
	}

	updatedByID, err := mapContestsByID(updated.Contests)
	if err != nil {
		p.logger.Printf("WARN group=%s updated standings have duplicate contest ids: %v", group.Slug, err)
		return domain.GeneratedGroupStandings{}, false
	}
	existingByID := map[string]domain.GeneratedContestStandings{}
	if hasExisting {
		existingByID, err = mapContestsByID(existing.Contests)
		if err != nil {
			p.logger.Printf("WARN group=%s existing standings have duplicate contest ids: %v", group.Slug, err)
			return domain.GeneratedGroupStandings{}, false
		}
	}

	solvedSummary := updated.SolvedSummary
	if hasNonUpdatedContests && hasExisting {
		solvedSummary = existing.SolvedSummary
	}

	merged := domain.GeneratedGroupStandings{
		GroupSlug:     group.Slug,
		GroupTitle:    group.Title,
		FormLink:      group.FormLink,
		SolvedSummary: solvedSummary,
		Contests:      make([]domain.GeneratedContestStandings, 0, len(group.Contests)),
	}

	for _, contestRef := range group.Contests {
		if contestRef.Update {
			if contest, ok := updatedByID[contestRef.ID]; ok {
				merged.Contests = append(merged.Contests, contest)
				continue
			}
			if hasExisting {
				if oldContest, oldOK := existingByID[contestRef.ID]; oldOK {
					p.logger.Printf("WARN group=%s contest=%s update=true but not built; keep previous generated version", group.Slug, contestRef.ID)
					merged.Contests = append(merged.Contests, oldContest)
					continue
				}
			}
			p.logger.Printf("WARN group=%s contest=%s update=true but not built and no previous version found", group.Slug, contestRef.ID)
			continue
		}

		contest, ok := existingByID[contestRef.ID]
		if !ok {
			p.logger.Printf("WARN group=%s contest=%s update=false but missing in previous standings", group.Slug, contestRef.ID)
			continue
		}
		merged.Contests = append(merged.Contests, contest)
	}

	return merged, true
}

func mapContestsByID(contests []domain.GeneratedContestStandings) (map[string]domain.GeneratedContestStandings, error) {
	out := make(map[string]domain.GeneratedContestStandings, len(contests))
	for _, contest := range contests {
		if _, exists := out[contest.ID]; exists {
			return nil, fmt.Errorf("duplicate contest id %q", contest.ID)
		}
		out[contest.ID] = contest
	}
	return out, nil
}

func mapGroupsBySlug(groups []domain.GroupDefinition) map[string]domain.GroupDefinition {
	out := make(map[string]domain.GroupDefinition, len(groups))
	for _, group := range groups {
		out[group.Slug] = group
	}
	return out
}

func filterGroupsToUpdate(groups []domain.GroupDefinition) []domain.GroupDefinition {
	out := make([]domain.GroupDefinition, 0, len(groups))
	for _, group := range groups {
		if group.Update {
			out = append(out, group)
		}
	}
	return out
}

func selectGroupsWithUpdatableContests(groups []domain.GroupDefinition) []domain.GroupDefinition {
	out := make([]domain.GroupDefinition, 0, len(groups))
	for _, group := range groups {
		contests := make([]domain.GroupContestRef, 0, len(group.Contests))
		for _, contest := range group.Contests {
			if contest.Update {
				contests = append(contests, contest)
			}
		}
		if len(contests) == 0 {
			continue
		}

		groupCopy := group
		groupCopy.Contests = contests
		out = append(out, groupCopy)
	}
	return out
}

func selectGroups(all []domain.GroupDefinition, onlyGroup string) []domain.GroupDefinition {
	if onlyGroup == "" {
		return all
	}
	out := make([]domain.GroupDefinition, 0, 1)
	for _, g := range all {
		if g.Slug == onlyGroup {
			out = append(out, g)
			break
		}
	}
	return out
}
