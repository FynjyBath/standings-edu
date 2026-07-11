package source

import "testing"

// Задача, решённая только «зачтено» (RUN_ACCEPTED=8), помечается Accepted; полный
// OK (0) — нет; OK после «зачтено» снимает пометку.
func TestInformaticsAcceptedFold(t *testing.T) {
	build := func(id int) string {
		return "https://informatics.msk.ru/mod/statements/view.php?chapterid=" + itoa(id) + "#1"
	}

	cases := []struct {
		name     string
		statuses []int
		want     bool // ждём Accepted у результата
	}{
		{"accepted only", []int{8}, true},
		{"full ok", []int{0}, false},
		{"accepted then ok", []int{8, 0}, false},
		{"ok then accepted", []int{0, 8}, false},
		{"partial only (not solved)", []int{7}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := map[string]informaticsTaskAggregate{}
			for _, st := range tc.statuses {
				foldInformaticsRun(informaticsRun{EjudgeStatus: st, Problem: informaticsProblem{ID: 100}, CreateTime: "2026-07-01T05:00:00+00:00"}, agg, build)
			}
			res := aggregatesToTaskResults(agg)
			if len(res) != 1 {
				t.Fatalf("want 1 result: %+v", res)
			}
			if res[0].Accepted != tc.want {
				t.Fatalf("Accepted=%v, want %v (statuses=%v)", res[0].Accepted, tc.want, tc.statuses)
			}
		})
	}
}

// «Ожидает подтверждения» (RUN_PENDING_REVIEW=16): решено (зелёный плюс), но без
// жёлтой рамки; «зачтено» (8) — решено и с рамкой; WA (5) — не решено.
func TestInformaticsPendingReviewFold(t *testing.T) {
	build := func(id int) string {
		return "https://informatics.msk.ru/mod/statements/view.php?chapterid=" + itoa(id) + "#1"
	}
	cases := []struct {
		name       string
		status     int
		wantSolved bool
		wantAccept bool
	}{
		{"ok", 0, true, false},
		{"accepted", 8, true, true},
		{"pending review", 16, true, false},
		{"pending (queued)", 11, false, false}, // транзиентный — не записываем
		{"wrong answer", 5, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := map[string]informaticsTaskAggregate{}
			foldInformaticsRun(informaticsRun{EjudgeStatus: tc.status, Problem: informaticsProblem{ID: 100}, CreateTime: "2026-07-01T05:00:00+00:00"}, agg, build)
			res := aggregatesToTaskResults(agg)
			if len(res) == 0 {
				if tc.wantSolved {
					t.Fatalf("ожидали результат для status=%d", tc.status)
				}
				return
			}
			if res[0].Solved != tc.wantSolved {
				t.Fatalf("status=%d Solved=%v, want %v", tc.status, res[0].Solved, tc.wantSolved)
			}
			if res[0].Accepted != tc.wantAccept {
				t.Fatalf("status=%d Accepted=%v, want %v", tc.status, res[0].Accepted, tc.wantAccept)
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}
