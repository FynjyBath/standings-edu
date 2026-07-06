package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"standings-edu/internal/domain"
)

func writeStandingsFile(t *testing.T, outDir, slug string, std domain.GeneratedGroupStandings, mod time.Time) {
	t.Helper()
	dir := filepath.Join(outDir, "standings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, slug+".json")
	blob, err := json.Marshal(std)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func stdWithTitle(title string, tasks int) domain.GeneratedGroupStandings {
	subTasks := make([]domain.GeneratedTask, tasks)
	for i := range subTasks {
		subTasks[i] = domain.GeneratedTask{Label: "A", URL: "https://acmp.ru/?id=1", NormalizedURL: "n"}
	}
	return domain.GeneratedGroupStandings{
		GroupSlug: "g1", GroupTitle: title,
		Contests: []domain.GeneratedContestStandings{{
			ID: "c1", Title: title,
			Tasks:       append([]domain.GeneratedTask(nil), subTasks...),
			Subcontests: []domain.GeneratedSubcontest{{Title: "S", Tasks: subTasks}},
			Rows:        []domain.GeneratedRow{{StudentID: "s1", SolvedCount: 1}},
			RowsFull:    []domain.GeneratedRow{{StudentID: "s1", SolvedCount: 5}},
		}},
	}
}

// Обновление файла подхватывается сервером: новый mtime → перечитывание.
func TestLoadGroupStandingsInvalidatesOnFileChange(t *testing.T) {
	out := t.TempDir()
	l := NewGeneratedLoader(out)

	t0 := time.Now().Add(-time.Hour)
	writeStandingsFile(t, out, "g1", stdWithTitle("v1", 2), t0)
	got, err := l.LoadGroupStandings("g1")
	if err != nil || got.GroupTitle != "v1" {
		t.Fatalf("first read: %v %q", err, got.GroupTitle)
	}

	// Тот же файл без изменений — из кэша, но данные те же.
	got, _ = l.LoadGroupStandings("g1")
	if got.GroupTitle != "v1" {
		t.Fatalf("cache hit returned wrong data: %q", got.GroupTitle)
	}

	// Перезапись с новым mtime — сервер обязан отдать новое.
	writeStandingsFile(t, out, "g1", stdWithTitle("v2", 3), t0.Add(time.Minute))
	got, _ = l.LoadGroupStandings("g1")
	if got.GroupTitle != "v2" {
		t.Fatalf("stale after rewrite (mtime): %q", got.GroupTitle)
	}

	// Изменение только размера при том же mtime — тоже инвалидация.
	same := t0.Add(time.Minute)
	writeStandingsFile(t, out, "g1", stdWithTitle("v3", 10), same)
	l.LoadGroupStandings("g1") // закэшировали v3@same
	writeStandingsFile(t, out, "g1", stdWithTitle("v4", 2), same)
	got, _ = l.LoadGroupStandings("g1")
	if got.GroupTitle != "v4" {
		t.Fatalf("stale after size change: %q", got.GroupTitle)
	}

	// Удаление файла — ошибка и очистка кэша.
	os.Remove(filepath.Join(out, "standings", "g1.json"))
	if _, err := l.LoadGroupStandings("g1"); err == nil {
		t.Fatal("missing file must error")
	}
}

// Клон-изоляция: мутация выданного значения не портит кэш и не влияет на
// параллельных читателей.
func TestLoadGroupStandingsCloneIsolation(t *testing.T) {
	out := t.TempDir()
	l := NewGeneratedLoader(out)
	writeStandingsFile(t, out, "g1", stdWithTitle("v1", 2), time.Now())

	a, _ := l.LoadGroupStandings("g1")
	// Сервер так мутирует: скрывает ссылки и подменяет строки полными.
	a.Contests[0].Tasks[0].URL = ""
	a.Contests[0].Subcontests[0].Tasks[0].URL = ""
	a.SwapInFullRows()

	b, _ := l.LoadGroupStandings("g1")
	if b.Contests[0].Tasks[0].URL == "" {
		t.Fatal("cache corrupted: task URL leaked from previous reader")
	}
	if b.Contests[0].Subcontests[0].Tasks[0].URL == "" {
		t.Fatal("cache corrupted: subcontest task URL leaked")
	}
	if b.Contests[0].RowsFull == nil || b.Contests[0].Rows[0].SolvedCount != 1 {
		t.Fatalf("cache corrupted: rows swapped for next reader: %+v", b.Contests[0])
	}
}

// Конкурентные чтения не гонятся (кэш под RWMutex, каждому — свой клон).
func TestLoadGroupStandingsConcurrent(t *testing.T) {
	out := t.TempDir()
	l := NewGeneratedLoader(out)
	writeStandingsFile(t, out, "g1", stdWithTitle("v1", 3), time.Now())

	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				got, err := l.LoadGroupStandings("g1")
				if err != nil || got.GroupTitle != "v1" {
					t.Errorf("concurrent read failed: %v %q", err, got.GroupTitle)
					return
				}
				got.Contests[0].Tasks[0].URL = "" // мутируем свой клон
				got.StripFullRows()
			}
		}()
	}
	for i := 0; i < 16; i++ {
		<-done
	}
}

// Round-trip профиля участника + отказ на небезопасный id.
func TestStudentProfileWriteRead(t *testing.T) {
	out := t.TempDir()
	w := NewGeneratedWriter(out)
	l := NewGeneratedLoader(out)

	p := domain.GeneratedStudentProfile{
		StudentID:  "ivanov-ii",
		PublicName: "Иванов И.",
		Stats:      domain.StudentActivityStats{TotalSolved: 7, TotalSubmissions: 20},
		Recent:     []domain.StudentSubmission{{Site: "codeforces", TaskURL: "u", Label: "CF 1A", Solved: true}},
	}
	if err := w.WriteStudentProfile(p); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := l.LoadStudentProfile("ivanov-ii")
	if err != nil || got.PublicName != "Иванов И." || got.Stats.TotalSolved != 7 || len(got.Recent) != 1 {
		t.Fatalf("read mismatch: %+v %v", got, err)
	}

	// Небезопасный id — отказ и в записи, и в чтении.
	if err := w.WriteStudentProfile(domain.GeneratedStudentProfile{StudentID: "../etc"}); err == nil {
		t.Fatal("write must reject unsafe id")
	}
	if _, err := l.LoadStudentProfile("../etc"); err == nil {
		t.Fatal("load must reject unsafe id")
	}
	// Отсутствующий профиль — os.ErrNotExist.
	if _, err := l.LoadStudentProfile("nobody"); err == nil {
		t.Fatal("missing profile must error")
	}
}
