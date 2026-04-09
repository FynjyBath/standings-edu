package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"standings-edu/internal/generator"
	providerbased "standings-edu/internal/provider_based"
	"standings-edu/internal/service"
	"standings-edu/internal/storage"
	tasksbased "standings-edu/internal/tasks_based"
)

func main() {
	var (
		dataDir          = flag.String("data", "./data", "path to source data directory")
		outDir           = flag.String("out", "./generated", "path to generated output directory")
		onlyGroup        = flag.String("group", "", "optional group slug to generate")
		parallelism      = flag.Int("parallel", 8, "max concurrent account fetches")
		cacheTTL         = flag.Duration("cache-ttl", 5*time.Minute, "TTL for account status cache")
		informaticsCreds = flag.String("informatics-creds", "./data/sites/informatics_credentials.json", "path to informatics credentials JSON")
		informaticsState = flag.String("informatics-state", "", "path to persisted informatics run_id state file (default: <out>/cache/informatics_runs_state.json)")
	)
	flag.Parse()

	if *informaticsState == "" {
		*informaticsState = filepath.Join(*outDir, "cache", "informatics_runs_state.json")
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	createdFiles, err := storage.EnsureInitialSourceFiles(*dataDir)
	if err != nil {
		logger.Fatalf("failed to initialize source files: %v", err)
	}
	for _, path := range createdFiles {
		logger.Printf("INFO initialized source file: %s", path)
	}

	infClient, err := tasksbased.NewInformaticsAPIClientFromFileWithState(*informaticsCreds, *informaticsState)
	if err != nil {
		logger.Fatalf("failed to init informatics client: %v", err)
	}
	cfClient := tasksbased.NewCodeforcesAPIClient()

	registry := tasksbased.NewRegistry()
	registry.Register("informatics", infClient)
	registry.Register("codeforces", cfClient)
	registry.Register("acmp", tasksbased.NewACMPClient())

	loader := storage.NewSourceLoader(*dataDir)
	writer := storage.NewGeneratedWriter(*outDir)
	providers := providerbased.NewContestProviderRegistry()
	providers.Register(providerbased.NewCodeforcesContestProvider(cfClient))
	providers.Register(providerbased.NewHTMLTableImportProvider())
	builder := service.NewStandingsBuilder(registry, providers, logger, *parallelism, *cacheTTL)
	gen := generator.New(loader, writer, builder, logger)

	if err := gen.Run(ctx, *onlyGroup); err != nil {
		logger.Fatalf("generation failed: %v", err)
	}
}
