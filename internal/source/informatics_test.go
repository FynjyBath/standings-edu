package source

import (
	"strconv"
	"testing"
	"time"
)

// create_time от informatics — момент в UTC: сейчас RFC3339 с "+00:00",
// наивные форматы (если формат сменят) тоже интерпретируются как UTC.
func TestParseInformaticsTime(t *testing.T) {
	want := time.Date(2026, 7, 5, 5, 28, 32, 0, time.UTC)

	cases := []string{
		"2026-07-05T05:28:32+00:00", // текущий формат API
		"2026-07-05T05:28:32Z",
		"2026-07-05T08:28:32+03:00", // явный сдвиг — тот же момент
		"2026-07-05T05:28:32",       // наивный ISO → UTC
		"2026-07-05 05:28:32",       // наивный с пробелом → UTC
	}
	for _, raw := range cases {
		got, ok := parseInformaticsTime(raw)
		if !ok || !got.Equal(want) {
			t.Fatalf("parse %q: got %v ok=%v, want %v", raw, got, ok, want)
		}
	}
}

// Зависшие в очереди/компиляции посылки не попадают в агрегаты (их финальный
// вердикт перезапишет запись при следующем заборе), но не мешают финальным.
func TestFoldStoredRunsSkipsPending(t *testing.T) {
	buildURL := func(problemID int) string { return "task/" + strconv.Itoa(problemID) }
	runs := []informaticsStoredRun{
		{ID: 100, Status: informaticsStatusOK, ProblemID: 1, Score: 100},
		{ID: 200, Status: 98, ProblemID: 2}, // компилируется
		{ID: 300, Status: informaticsStatusOK, ProblemID: 3, Score: 100},
		{ID: 400, Status: informaticsStatusAccepted, ProblemID: 4, Score: 100},
		{ID: 500, Status: 11, ProblemID: 5}, // в очереди
		{ID: 600, Status: informaticsStatusOK, ProblemID: 6, Score: 100},
	}
	agg := make(map[string]informaticsTaskAggregate)
	foldStoredInformaticsRuns(runs, agg, buildURL)

	for _, pid := range []int{1, 3, 4, 6} {
		a, ok := agg[buildURL(pid)]
		if !ok || !a.solved {
			t.Fatalf("задача %d должна быть решена: %+v", pid, a)
		}
	}
	if _, ok := agg[buildURL(2)]; ok {
		t.Fatal("задача 2 (compiling) не должна попасть в результат")
	}
	if _, ok := agg[buildURL(5)]; ok {
		t.Fatal("задача 5 (в очереди) не должна попасть в результат")
	}
}
