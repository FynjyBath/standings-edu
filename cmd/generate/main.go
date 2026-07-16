package main

import (
	"context"
	"errors"
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
		informaticsCreds = flag.String("informatics-creds-file", "./data/credentials/informatics_credentials.json", "path to informatics credentials JSON")
		codeforcesCreds  = flag.String("codeforces-creds-file", "./data/credentials/codeforces_credentials.json", "path to optional codeforces credentials JSON")
		ejudgeCreds      = flag.String("ejudge-creds-file", "./data/credentials/ejudge_credentials.json", "path to optional ejudge instances JSON (array)")
		moodleCreds      = flag.String("moodle-creds-file", "./data/credentials/moodle_credentials.json", "path to optional moodle credentials JSON (base_url + token or username/password)")
		informaticsState = flag.String("informatics-state", "", "path to persisted informatics run_id state file (default: <out>/cache/informatics_runs_state.json)")
		codeforcesState  = flag.String("codeforces-state", "", "path to persisted codeforces submission_id state file (default: <out>/cache/codeforces_user_status_state.json)")
		refreshTasks     = flag.Bool("refresh-tasks", false, "re-fetch contest task lists/titles from sites instead of using the on-disk tasks cache")
	)
	flag.Parse()

	if *informaticsState == "" {
		*informaticsState = filepath.Join(*outDir, "cache", "informatics_runs_state.json")
	}
	if *codeforcesState == "" {
		*codeforcesState = filepath.Join(*outDir, "cache", "codeforces_user_status_state.json")
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfClient, err := source.NewCodeforcesAPIClientFromFileWithState(*codeforcesCreds, *codeforcesState)
	if err != nil {
		logger.Fatalf("failed to init codeforces client: %v", err)
	}

	registry := source.NewRegistry()
	registry.RegisterSite("codeforces", cfClient)
	registry.RegisterSite("acmp", source.NewACMPClient())
	registry.RegisterProvider(source.NewCodeforcesContestProvider(cfClient))
	registry.RegisterProvider(source.NewHTMLTableImportProvider())
	registry.RegisterProvider(source.NewManualTableProvider())

	// informatics требует логина, поэтому без файла credentials источник просто
	// отключаем (как codeforces в anonymous-режиме), а не падаем. Если файл есть,
	// но битый — это ошибка конфигурации, останавливаемся.
	infClient, err := source.NewInformaticsAPIClientFromFileWithState(*informaticsCreds, *informaticsState)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Printf("WARN informatics credentials file %q not found; informatics source disabled", *informaticsCreds)
		} else {
			logger.Fatalf("failed to init informatics client: %v", err)
		}
	} else {
		// Кэш состава задач (оглавления сборников, названия задач) — на диске
		// рядом с state; -refresh-tasks принудительно перечитывает с сайта.
		infClient.ConfigureTasksCache(filepath.Join(*outDir, "cache", "informatics_tasks_cache.json"), *refreshTasks)
		registry.RegisterSite("informatics", infClient)
	}

	// Любые ejudge из ejudge_credentials.json: каждый регистрируется как отдельный
	// сайт (site = ejudge_id), сопоставление по хосту ссылки.
	ejInstances, err := source.LoadEjudgeInstances(*ejudgeCreds)
	if err != nil {
		logger.Fatalf("failed to load ejudge credentials: %v", err)
	}
	for _, cfg := range ejInstances {
		ejClient, err := source.NewEjudgeClient(cfg)
		if err != nil {
			logger.Fatalf("failed to init ejudge %q: %v", cfg.EjudgeID, err)
		}
		ejClient.SetLogger(logger)
		registry.RegisterSite(ejClient.SiteName(), ejClient)
		logger.Printf("INFO ejudge instance %q (%s) registered", cfg.EjudgeID, cfg.BaseURL)
	}

	// Moodle: провайдер таблиц из журнала оценок. Без файла кредов — просто
	// отключён (как informatics), битый файл — ошибка конфигурации.
	moodleCredentials, err := source.LoadMoodleCredentials(*moodleCreds)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Printf("WARN moodle credentials file %q not found; moodle_grades provider disabled", *moodleCreds)
		} else {
			logger.Fatalf("failed to load moodle credentials: %v", err)
		}
	} else {
		registry.RegisterProvider(source.NewMoodleGradesProvider(source.NewMoodleClient(moodleCredentials)))
		logger.Printf("INFO moodle_grades provider registered (%s)", moodleCredentials.BaseURL)
	}

	loader := storage.NewSourceLoader(*dataDir)
	writer := storage.NewGeneratedWriter(*outDir)
	builder := standings.NewBuilder(registry, logger, *parallelism)
	pipeline := standings.NewPipeline(loader, writer, builder, logger)

	if err := pipeline.Run(ctx, *onlyGroup); err != nil {
		logger.Fatalf("generation failed: %v", err)
	}
}
