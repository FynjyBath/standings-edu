package source

import "testing"

// Незавершённые посылки (в очереди/на тестировании) не должны запоминаться:
// водяной знак не должен доходить до самой ранней из них, иначе, став OK, они
// будут пропущены при следующем сборе (id <= известного) и зачёт потеряется.
func TestFoldNewCodeforcesSubmissionsPending(t *testing.T) {
	probA := codeforcesProblem{ContestID: 1711, Index: "A"}
	probB := codeforcesProblem{ContestID: 1711, Index: "B"}
	probC := codeforcesProblem{ContestID: 1711, Index: "C"}
	urlA := buildCodeforcesProblemURL(probA)
	urlB := buildCodeforcesProblemURL(probB)

	t.Run("newest is testing -> watermark stays below it", func(t *testing.T) {
		agg := make(map[string]codeforcesTaskAggregate)
		subs := []codeforcesSubmission{
			{ID: 102, Verdict: "TESTING", Problem: probB},
			{ID: 101, Verdict: "OK", Problem: probA},
		}
		max, folded := foldNewCodeforcesSubmissions(subs, 100, agg)
		if max != 101 {
			t.Fatalf("watermark must not pass pending 102: got %d", max)
		}
		if !folded {
			t.Fatal("terminal 101 must be folded")
		}
		if !agg[urlA].solved {
			t.Fatal("A must be solved")
		}
		if _, ok := agg[urlB]; ok {
			t.Fatal("pending B must not be recorded at all")
		}
	})

	t.Run("only submission is in queue (empty verdict) -> nothing remembered", func(t *testing.T) {
		agg := make(map[string]codeforcesTaskAggregate)
		subs := []codeforcesSubmission{{ID: 101, Verdict: "", Problem: probA}}
		max, folded := foldNewCodeforcesSubmissions(subs, 100, agg)
		if max != 100 || folded {
			t.Fatalf("in-queue submission must not advance watermark or fold: max=%d folded=%v", max, folded)
		}
		if len(agg) != 0 {
			t.Fatalf("nothing must be recorded: %+v", agg)
		}
	})

	t.Run("terminal above a pending one is deferred (no duplicate later)", func(t *testing.T) {
		agg := make(map[string]codeforcesTaskAggregate)
		subs := []codeforcesSubmission{
			{ID: 103, Verdict: "OK", Problem: probA},
			{ID: 102, Verdict: "TESTING", Problem: probB},
			{ID: 101, Verdict: "OK", Problem: probC},
		}
		max, folded := foldNewCodeforcesSubmissions(subs, 100, agg)
		if max != 101 {
			t.Fatalf("watermark must stop below earliest pending 102: got %d", max)
		}
		if !folded {
			t.Fatal("101 must be folded")
		}
		// 103 (terminal, above the pending floor) must NOT be folded this pass —
		// it will be re-fetched next time, avoiding a double-counted submission.
		if _, ok := agg[urlA]; ok {
			t.Fatal("terminal submission above pending floor must be deferred")
		}
	})

	t.Run("no pending -> all folded, watermark is max id", func(t *testing.T) {
		agg := make(map[string]codeforcesTaskAggregate)
		subs := []codeforcesSubmission{
			{ID: 103, Verdict: "WRONG_ANSWER", Problem: probA},
			{ID: 102, Verdict: "OK", Problem: probA},
		}
		max, folded := foldNewCodeforcesSubmissions(subs, 100, agg)
		if max != 103 || !folded {
			t.Fatalf("all terminal must fold, watermark=max: got %d folded=%v", max, folded)
		}
		if !agg[urlA].solved {
			t.Fatal("A must be solved (from 102)")
		}
	})
}

func TestIsCodeforcesTerminalVerdict(t *testing.T) {
	terminal := []string{"OK", "WRONG_ANSWER", "TIME_LIMIT_EXCEEDED", "COMPILATION_ERROR", "PARTIAL", "RUNTIME_ERROR"}
	for _, v := range terminal {
		if !isCodeforcesTerminalVerdict(v) {
			t.Errorf("%q must be terminal", v)
		}
	}
	pending := []string{"", "  ", "TESTING", "testing"}
	for _, v := range pending {
		if isCodeforcesTerminalVerdict(v) {
			t.Errorf("%q must be non-terminal", v)
		}
	}
}

// v6: незавершённые посылки хранятся, но в агрегаты не попадают; их вердикт
// перезапишется свежими данными при следующем заборе (см. TestInformaticsVerdictOverwrite).
func TestFoldStoredInformaticsRunsPending(t *testing.T) {
	client := &InformaticsAPIClient{baseURL: "https://informatics.msk.ru"}
	build := client.buildTaskURL

	agg := make(map[string]informaticsTaskAggregate)
	foldStoredInformaticsRuns([]informaticsStoredRun{
		{ID: 502, Status: 98, ProblemID: 20},            // компилируется
		{ID: 501, Status: 0, ProblemID: 10, Score: 100}, // OK
	}, agg, build)
	if !agg[build(10)].solved {
		t.Fatal("problem 10 must be solved")
	}
	if _, ok := agg[build(20)]; ok {
		t.Fatal("compiling run must not be recorded in aggregates")
	}
}
func TestIsInformaticsPendingStatus(t *testing.T) {
	pending := []int{11, 96, 97, 98, 99}
	for _, s := range pending {
		if !isInformaticsPendingStatus(s) {
			t.Errorf("status %d must be pending", s)
		}
	}
	// В т.ч. финальные статусы с большими кодами (520 реально встречается у
	// informatics) — их нельзя считать незавершёнными, иначе целые аккаунты
	// теряются: один старый run в таком статусе опускает порог и вырезает всё
	// новее него.
	terminal := []int{0, 1, 2, 3, 5, 7, 8, 9, 10, 13, 16, 95, 100, 101, 520}
	for _, s := range terminal {
		if isInformaticsPendingStatus(s) {
			t.Errorf("status %d must be terminal", s)
		}
	}
}
