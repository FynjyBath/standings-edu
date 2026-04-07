package generator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/service"
	"standings-edu/internal/storage"
)

type Generator struct {
	loader  *storage.SourceLoader
	writer  *storage.GeneratedWriter
	builder *service.StandingsBuilder
	logger  *log.Logger
}

func New(loader *storage.SourceLoader, writer *storage.GeneratedWriter, builder *service.StandingsBuilder, logger *log.Logger) *Generator {
	if logger == nil {
		logger = log.Default()
	}
	return &Generator{
		loader:  loader,
		writer:  writer,
		builder: builder,
		logger:  logger,
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

	updatedOverall, standingsByGroup, err := g.builder.BuildAllStandings(ctx, source, groupsToUpdate)
	if err != nil {
		return fmt.Errorf("build standings: %w", err)
	}

	overallToWrite := updatedOverall
	if shouldMergeOverall(onlyGroup, source.Groups, groupsToUpdate) {
		merged, mergeErr := g.mergeOverallWithExisting(updatedOverall)
		if mergeErr != nil {
			g.logger.Printf("WARN failed to merge summary with existing data: %v; writing updated subset only", mergeErr)
		} else {
			overallToWrite = merged
		}
	}

	if err := g.writer.WriteOverallStandings(overallToWrite); err != nil {
		return fmt.Errorf("write overall standings: %w", err)
	}

	metas := make([]domain.GeneratedGroupMeta, 0, len(selectedGroups))
	for _, group := range selectedGroups {
		metas = append(metas, domain.GeneratedGroupMeta{Slug: group.Slug, Title: group.Title})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	if err := g.writer.WriteGroups(metas); err != nil {
		return fmt.Errorf("write groups list: %w", err)
	}

	generatedCount := 0
	for _, group := range groupsToUpdate {
		g.logger.Printf("INFO generating standings for group=%s", group.Slug)

		standings, ok := standingsByGroup[group.Slug]
		if !ok {
			g.logger.Printf("ERROR group=%s build result not found", group.Slug)
			continue
		}

		if err := g.writer.WriteGroupStandings(standings); err != nil {
			g.logger.Printf("ERROR group=%s write standings failed: %v", group.Slug, err)
			continue
		}

		generatedCount++
		g.logger.Printf("INFO group=%s generated", group.Slug)
	}

	if len(groupsToUpdate) > 0 && generatedCount == 0 {
		return fmt.Errorf("no groups generated successfully")
	}

	g.logger.Printf("INFO generation complete: updated %d/%d selected groups", generatedCount, len(groupsToUpdate))
	return nil
}

func shouldMergeOverall(onlyGroup string, allGroups []domain.GroupDefinition, groupsToUpdate []domain.GroupDefinition) bool {
	if onlyGroup != "" {
		return true
	}
	return len(groupsToUpdate) < len(allGroups)
}

func (g *Generator) mergeOverallWithExisting(updated domain.GeneratedOverallStandings) (domain.GeneratedOverallStandings, error) {
	loader := storage.NewGeneratedLoader(g.writer.OutDir)
	existing, err := loader.LoadOverallStandings()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return updated, nil
		}
		return domain.GeneratedOverallStandings{}, err
	}

	rowsByStudentID := make(map[string]domain.GeneratedOverallRow, len(existing.Rows)+len(updated.Rows))
	for _, row := range existing.Rows {
		rowsByStudentID[row.StudentID] = remapOverallRowToSites(row, existing.Sites, updated.Sites)
	}
	for _, row := range updated.Rows {
		rowsByStudentID[row.StudentID] = row
	}

	mergedRows := make([]domain.GeneratedOverallRow, 0, len(rowsByStudentID))
	for _, row := range rowsByStudentID {
		mergedRows = append(mergedRows, row)
	}

	sort.Slice(mergedRows, func(i, j int) bool {
		if mergedRows[i].TotalSolved != mergedRows[j].TotalSolved {
			return mergedRows[i].TotalSolved > mergedRows[j].TotalSolved
		}
		return strings.ToLower(mergedRows[i].FullName) < strings.ToLower(mergedRows[j].FullName)
	})

	return domain.GeneratedOverallStandings{Sites: updated.Sites, Rows: mergedRows}, nil
}

func remapOverallRowToSites(row domain.GeneratedOverallRow, fromSites []string, toSites []string) domain.GeneratedOverallRow {
	countsBySite := make(map[string]int, len(fromSites))
	for i, site := range fromSites {
		if i >= len(row.SolvedBySite) {
			break
		}
		countsBySite[site] = row.SolvedBySite[i]
	}

	perSite := make([]int, len(toSites))
	total := 0
	for i, site := range toSites {
		count := countsBySite[site]
		perSite[i] = count
		total += count
	}

	row.SolvedBySite = perSite
	row.TotalSolved = total
	return row
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
