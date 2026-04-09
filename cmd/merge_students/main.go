package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"standings-edu/internal/studentintake"
)

func main() {
	var (
		dataDir      = flag.String("data", "./data", "path to data directory")
		studentsPath = flag.String("students", "", "path to students.json (default: <data>/students.json)")
		intakePath   = flag.String("intake", "", "path to student_intake.json (default: <data>/student_intake.json)")
		dryRun       = flag.Bool("dry-run", true, "show merge result without writing students.json")
		writeMode    = flag.Bool("write", false, "write merged students.json")
	)
	flag.Parse()

	if *studentsPath == "" {
		*studentsPath = filepath.Join(*dataDir, "students.json")
	}
	if *intakePath == "" {
		*intakePath = filepath.Join(*dataDir, "student_intake.json")
	}
	if *writeMode {
		*dryRun = false
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)

	students, err := studentintake.LoadStudentsFile(*studentsPath)
	if err != nil {
		logger.Fatalf("failed to load students: %v", err)
	}

	intake, err := studentintake.LoadIntakeFile(*intakePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Fatalf("intake file %q not found", *intakePath)
		}
		logger.Fatalf("failed to load intake: %v", err)
	}

	merged, stats, err := studentintake.MergeStudents(students, intake)
	if err != nil {
		logger.Fatalf("merge failed: %v", err)
	}

	if *dryRun {
		fmt.Printf("dry-run: students=%d updated=%d added=%d result=%d\n", len(students), stats.Updated, stats.Added, len(merged))
		return
	}

	if err := studentintake.WriteStudentsFile(*studentsPath, merged); err != nil {
		logger.Fatalf("write merged students failed: %v", err)
	}
	fmt.Printf("merged: students=%d updated=%d added=%d result=%d file=%s\n", len(students), stats.Updated, stats.Added, len(merged), *studentsPath)
}
