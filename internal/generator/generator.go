package generator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"

	"standings-edu/internal/domain"
	"standings-edu/internal/service"
	"standings-edu/internal/storage"
)

type Generator struct {
	loader          *storage.SourceLoader
	writer          *storage.GeneratedWriter
	generatedLoader *storage.GeneratedLoader
	builder         *service.StandingsBuilder
	logger          *log.Logger
}

func New(loader *storage.SourceLoader, writer *storage.GeneratedWriter, builder *service.StandingsBuilder, logger *log.Logger) *Generator {
	if logger == nil {
		logger = log.Default()
	}
	return &Generator{
		loader:          loader,
		writer:          writer,
		generatedLoader: storage.NewGeneratedLoader(writer.OutDir),
		builder:         builder,
		logger:          logger,
	}
}

func (g *Generator) Run(ctx context.Context, onlyGroup string) error {
	source, err := g.loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("load source data: %w", err)
	}

	selectedGroups := selectGroups(source.Groups, onlyGroup)
	if onlyGroup != "" && len(selectedGroups) == 0 {
		return fmt.Errorf("group %q not found", onlyGroup)
	}

	groupsToUpdate := filterGroupsToUpdate(selectedGroups)
	if len(groupsToUpdate) == 0 {
		g.logger.Printf("INFO no groups with update=true selected; nothing to update")
		return nil
	}

	buildGroups := selectGroupsWithUpdatableContests(groupsToUpdate)
	if len(buildGroups) == 0 {
		g.logger.Printf("INFO no contests with update=true in selected groups; nothing to update")
		return nil
	}

	standingsByGroup, err := g.builder.BuildGroupsStandings(ctx, source, buildGroups)
	if err != nil {
		return fmt.Errorf("build standings: %w", err)
	}

	metas := make([]domain.GeneratedGroupMeta, 0, len(selectedGroups))
	for _, group := range selectedGroups {
		metas = append(metas, domain.GeneratedGroupMeta{Slug: group.Slug, Title: group.Title})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	if err := g.writer.WriteGroups(metas); err != nil {
		return fmt.Errorf("write groups list: %w", err)
	}

	fullGroupBySlug := make(map[string]domain.GroupDefinition, len(groupsToUpdate))
	for _, group := range groupsToUpdate {
		fullGroupBySlug[group.Slug] = group
	}

	generatedCount := 0
	for _, group := range buildGroups {
		g.logger.Printf("INFO generating standings for group=%s", group.Slug)

		updatedStandings, ok := standingsByGroup[group.Slug]
		if !ok {
			g.logger.Printf("ERROR group=%s build result not found", group.Slug)
			continue
		}

		fullGroup := fullGroupBySlug[group.Slug]
		mergedStandings, ok := g.mergeWithNonUpdatedContests(fullGroup, updatedStandings)
		if !ok {
			g.logger.Printf("ERROR group=%s merge failed; skip writing to avoid data loss", group.Slug)
			continue
		}

		if err := g.writer.WriteGroupStandings(mergedStandings); err != nil {
			g.logger.Printf("ERROR group=%s write standings failed: %v", group.Slug, err)
			continue
		}

		generatedCount++
		g.logger.Printf("INFO group=%s generated", group.Slug)
	}

	if generatedCount == 0 {
		return fmt.Errorf("no groups generated successfully")
	}

	g.logger.Printf("INFO generation complete: updated %d/%d selected groups", generatedCount, len(buildGroups))
	return nil
}

func (g *Generator) mergeWithNonUpdatedContests(group domain.GroupDefinition, updated domain.GeneratedGroupStandings) (domain.GeneratedGroupStandings, bool) {
	hasNonUpdatedContests := false
	for _, contest := range group.Contests {
		if !contest.Update {
			hasNonUpdatedContests = true
			break
		}
	}

	existing := domain.GeneratedGroupStandings{}
	hasExisting := false
	existingLoaded, loadErr := g.generatedLoader.LoadGroupStandings(group.Slug)
	if loadErr == nil {
		existing = existingLoaded
		hasExisting = true
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		g.logger.Printf("WARN group=%s load existing standings failed: %v", group.Slug, loadErr)
	}

	if hasNonUpdatedContests && !hasExisting {
		if errors.Is(loadErr, os.ErrNotExist) {
			g.logger.Printf("WARN group=%s has contests with update=false but previous standings are missing", group.Slug)
		}
		return domain.GeneratedGroupStandings{}, false
	}

	updatedByID := makeContestBuckets(updated.Contests)
	existingByID := map[string][]domain.GeneratedContestStandings{}
	if hasExisting {
		existingByID = makeContestBuckets(existing.Contests)
	}

	merged := domain.GeneratedGroupStandings{
		GroupSlug:  group.Slug,
		GroupTitle: group.Title,
		Contests:   make([]domain.GeneratedContestStandings, 0, len(group.Contests)),
	}

	for _, contestRef := range group.Contests {
		if contestRef.Update {
			contest, ok := takeFirstContest(updatedByID, contestRef.ID)
			if !ok {
				if hasExisting {
					existingContest, oldOK := takeFirstContest(existingByID, contestRef.ID)
					if oldOK {
						g.logger.Printf("WARN group=%s contest=%s update=true but not built; keep previous generated version", group.Slug, contestRef.ID)
						merged.Contests = append(merged.Contests, existingContest)
						continue
					}
				}
				g.logger.Printf("WARN group=%s contest=%s update=true but not built and no previous version found", group.Slug, contestRef.ID)
				continue
			}
			merged.Contests = append(merged.Contests, contest)
			continue
		}

		contest, ok := takeFirstContest(existingByID, contestRef.ID)
		if !ok {
			g.logger.Printf("WARN group=%s contest=%s update=false but missing in previous standings", group.Slug, contestRef.ID)
			continue
		}
		merged.Contests = append(merged.Contests, contest)
	}

	return merged, true
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

func makeContestBuckets(contests []domain.GeneratedContestStandings) map[string][]domain.GeneratedContestStandings {
	buckets := make(map[string][]domain.GeneratedContestStandings, len(contests))
	for _, contest := range contests {
		buckets[contest.ID] = append(buckets[contest.ID], contest)
	}
	return buckets
}

func takeFirstContest(buckets map[string][]domain.GeneratedContestStandings, contestID string) (domain.GeneratedContestStandings, bool) {
	list := buckets[contestID]
	if len(list) == 0 {
		return domain.GeneratedContestStandings{}, false
	}
	item := list[0]
	if len(list) == 1 {
		delete(buckets, contestID)
	} else {
		buckets[contestID] = list[1:]
	}
	return item, true
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
