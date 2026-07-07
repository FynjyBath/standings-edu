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

func TestFoldNewInformaticsRunsPending(t *testing.T) {
	client := &InformaticsAPIClient{baseURL: "https://informatics.msk.ru"}
	build := client.buildTaskURL

	t.Run("newest is compiling -> watermark stays below it", func(t *testing.T) {
		agg := make(map[string]informaticsTaskAggregate)
		runs := []informaticsRun{
			{ID: 502, EjudgeStatus: 98, Problem: informaticsProblem{ID: 20}}, // компилируется
			{ID: 501, EjudgeStatus: 0, Problem: informaticsProblem{ID: 10}},  // OK
		}
		max := foldNewInformaticsRuns(runs, 500, agg, build)
		if max != 501 {
			t.Fatalf("watermark must not pass compiling run 502: got %d", max)
		}
		if !agg[build(10)].solved {
			t.Fatal("problem 10 must be solved")
		}
		if _, ok := agg[build(20)]; ok {
			t.Fatal("compiling run must not be recorded")
		}
	})

	t.Run("only run is in queue -> nothing remembered", func(t *testing.T) {
		agg := make(map[string]informaticsTaskAggregate)
		runs := []informaticsRun{{ID: 501, EjudgeStatus: 11, Problem: informaticsProblem{ID: 10}}} // pending
		max := foldNewInformaticsRuns(runs, 500, agg, build)
		if max != 500 {
			t.Fatalf("pending run must not advance watermark: got %d", max)
		}
		if len(agg) != 0 {
			t.Fatalf("nothing must be recorded: %+v", agg)
		}
	})

	t.Run("accepted (status 8) is terminal and counted", func(t *testing.T) {
		agg := make(map[string]informaticsTaskAggregate)
		runs := []informaticsRun{{ID: 501, EjudgeStatus: 8, Problem: informaticsProblem{ID: 10}}}
		max := foldNewInformaticsRuns(runs, 500, agg, build)
		if max != 501 || !agg[build(10)].solved {
			t.Fatalf("accepted must be counted: max=%d agg=%+v", max, agg)
		}
	})
}

func TestIsInformaticsPendingStatus(t *testing.T) {
	pending := []int{11, 95, 96, 97, 98, 99, 100}
	for _, s := range pending {
		if !isInformaticsPendingStatus(s) {
			t.Errorf("status %d must be pending", s)
		}
	}
	terminal := []int{0, 1, 2, 3, 5, 7, 8, 9, 10, 13, 16}
	for _, s := range terminal {
		if isInformaticsPendingStatus(s) {
			t.Errorf("status %d must be terminal", s)
		}
	}
}
