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

	for _, raw := range []string{"", "вчера", "05:28:32"} {
		if _, ok := parseInformaticsTime(raw); ok {
			t.Fatalf("parse %q must fail", raw)
		}
	}
}

// Регресс: одна старая посылка, застрявшая в очереди/на компиляции (низкий
// run.ID, статус 11/98), НЕ должна обнулять все более новые результаты аккаунта.
// Раньше foldNewInformaticsRuns пропускал всё с run.ID >= самой ранней
// незавершённой — из-за древней зависшей посылки терялись почти все задачи.
func TestFoldPendingRunDoesNotDropNewer(t *testing.T) {
	buildURL := func(problemID int) string { return "task/" + strconv.Itoa(problemID) }
	runs := []informaticsRun{
		{ID: 100, EjudgeStatus: informaticsStatusOK, Problem: informaticsProblem{ID: 1}},       // старое решено
		{ID: 200, EjudgeStatus: 98, Problem: informaticsProblem{ID: 2}},                        // ЗАСТРЯЛО (compiling)
		{ID: 300, EjudgeStatus: informaticsStatusOK, Problem: informaticsProblem{ID: 3}},       // новее зависшей — решено
		{ID: 400, EjudgeStatus: informaticsStatusAccepted, Problem: informaticsProblem{ID: 4}}, // зачтено
		{ID: 500, EjudgeStatus: 11, Problem: informaticsProblem{ID: 5}},                        // ещё одно в очереди
		{ID: 600, EjudgeStatus: informaticsStatusOK, Problem: informaticsProblem{ID: 6}},       // самое новое — решено
	}
	agg := make(map[string]informaticsTaskAggregate)
	maxRunID := foldNewInformaticsRuns(runs, 0, agg, buildURL)

	// Свёрнуты все ФИНАЛЬНЫЕ задачи (1,3,4,6) несмотря на зависшие 2 и 5.
	for _, pid := range []int{1, 3, 4, 6} {
		a, ok := agg[buildURL(pid)]
		if !ok || !a.solved {
			t.Fatalf("задача %d должна быть решена (зависшая посылка не должна её терять): %+v", pid, a)
		}
	}
	// Незавершённые (2, 5) не записаны.
	if _, ok := agg[buildURL(2)]; ok {
		t.Fatal("задача 2 (compiling) не должна попасть в результат")
	}
	if _, ok := agg[buildURL(5)]; ok {
		t.Fatal("задача 5 (в очереди) не должна попасть в результат")
	}
	// Водяной знак не перепрыгнул самую раннюю незавершённую (run.ID=200),
	// чтобы её финализацию перечитали позже.
	if maxRunID != 100 {
		t.Fatalf("водяной знак должен остаться ниже самой ранней незавершённой (200), got %d", maxRunID)
	}
}
