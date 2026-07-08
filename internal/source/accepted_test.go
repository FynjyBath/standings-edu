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
