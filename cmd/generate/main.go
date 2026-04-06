package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"standings-edu/internal/generator"
	"standings-edu/internal/service"
	"standings-edu/internal/sites"
	"standings-edu/internal/storage"
)

func main() {
	var (
		dataDir     = flag.String("data", "./data", "path to source data directory")
		outDir      = flag.String("out", "./generated", "path to generated output directory")
		onlyGroup   = flag.String("group", "", "optional group slug to generate")
		parallelism = flag.Int("parallel", 8, "max concurrent account fetches")
		cacheTTL    = flag.Duration("cache-ttl", 5*time.Minute, "TTL for account status cache")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry := sites.NewRegistry()
	registry.Register("informatics", sites.NewInformaticsStubClient())
	registry.Register("codeforces", sites.NewCodeforcesStubClient())

	loader := storage.NewSourceLoader(*dataDir)
	writer := storage.NewGeneratedWriter(*outDir)
	builder := service.NewStandingsBuilder(registry, logger, *parallelism, *cacheTTL)
	gen := generator.New(loader, writer, builder, logger)

	if err := gen.Run(ctx, *onlyGroup); err != nil {
		logger.Fatalf("generation failed: %v", err)
	}
}
