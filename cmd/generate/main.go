package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"standings-edu/internal/source"
	"standings-edu/internal/standings"
	"standings-edu/internal/storage"
)

func main() {
	var (
		dataDir          = flag.String("data-dir", "./data", "path to source data directory")
		outDir           = flag.String("generated-dir", "./generated", "path to generated output directory")
		onlyGroup        = flag.String("group", "", "optional group slug to generate")
		parallelism      = flag.Int("parallelism", 8, "max concurrent account fetches")
		informaticsCreds = flag.String("informatics-creds-file", "./data/sites/informatics_credentials.json", "path to informatics credentials JSON")
		informaticsState = flag.String("informatics-state", "", "path to persisted informatics run_id state file (default: <out>/cache/informatics_runs_state.json)")
	)
	flag.Parse()

	if *informaticsState == "" {
		*informaticsState = filepath.Join(*outDir, "cache", "informatics_runs_state.json")
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	infClient, err := source.NewInformaticsAPIClientFromFileWithState(*informaticsCreds, *informaticsState)
	if err != nil {
		logger.Fatalf("failed to init informatics client: %v", err)
	}
	cfClient := source.NewCodeforcesAPIClient()

	registry := source.NewRegistry()
	registry.RegisterSite("informatics", infClient)
	registry.RegisterSite("codeforces", cfClient)
	registry.RegisterSite("acmp", source.NewACMPClient())
	registry.RegisterProvider(source.NewCodeforcesContestProvider(cfClient))
	registry.RegisterProvider(source.NewHTMLTableImportProvider())

	loader := storage.NewSourceLoader(*dataDir)
	writer := storage.NewGeneratedWriter(*outDir)
	builder := standings.NewBuilder(registry, logger, *parallelism)
	pipeline := standings.NewPipeline(loader, writer, builder, logger)

	if err := pipeline.Run(ctx, *onlyGroup); err != nil {
		logger.Fatalf("generation failed: %v", err)
	}
}
