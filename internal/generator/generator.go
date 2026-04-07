package generator

import (
	"context"
	"fmt"
	"log"
	"sort"

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

	groups := selectGroups(source.Groups, onlyGroup)
	if onlyGroup != "" && len(groups) == 0 {
		return fmt.Errorf("group %q not found", onlyGroup)
	}

	overall, standingsByGroup, err := g.builder.BuildAllStandings(ctx, source, groups)
	if err != nil {
		return fmt.Errorf("build standings: %w", err)
	}
	if err := g.writer.WriteOverallStandings(overall); err != nil {
		return fmt.Errorf("write overall standings: %w", err)
	}

	metas := make([]domain.GeneratedGroupMeta, 0, len(groups))
	generatedCount := 0
	for _, group := range groups {
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

		metas = append(metas, domain.GeneratedGroupMeta{
			Slug:  group.Slug,
			Title: group.Title,
		})
		generatedCount++
		g.logger.Printf("INFO group=%s generated", group.Slug)
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].Slug < metas[j].Slug
	})

	if err := g.writer.WriteGroups(metas); err != nil {
		return fmt.Errorf("write groups list: %w", err)
	}

	if len(groups) > 0 && generatedCount == 0 {
		return fmt.Errorf("no groups generated successfully")
	}

	g.logger.Printf("INFO generation complete: %d/%d groups", generatedCount, len(groups))
	return nil
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
