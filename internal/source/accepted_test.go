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
			for i, st := range tc.statuses {
				foldStoredInformaticsRuns([]informaticsStoredRun{{ID: i + 1, ProblemID: 100, Status: st}}, agg, build)
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

// informatics: OK (0) — решено без рамки; «зачтено» (8) — решено с жёлтой рамкой;
// «ожидает подтверждения» (16) на информатиксе не засчитываем; WA (5) — не решено.
func TestInformaticsFoldStatuses(t *testing.T) {
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
		{"accepted (зачтено)", 8, true, true},
		{"pending review", 16, false, false}, // informatics: не решено
		{"pending (queued)", 11, false, false},
		{"wrong answer", 5, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := map[string]informaticsTaskAggregate{}
			foldStoredInformaticsRuns([]informaticsStoredRun{{ID: 1, ProblemID: 100, Status: tc.status}}, agg, build)
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

// Провайдер-специфичная трактовка рамки: informatics — рамка у «зачтено» (8),
// OK её перебивает; ejudge — рамка у OK (0), «ожидает подтверждения» (16) без
// рамки, но засчитывается как решено.
func TestBorderStatusPerProvider(t *testing.T) {
	// informatics
	if !isInformaticsBorderStatus(8) || isInformaticsBorderStatus(0) || isInformaticsBorderStatus(16) {
		t.Fatal("informatics: рамка только у 8 (зачтено)")
	}
	if !isInformaticsSuppressStatus(0) || isInformaticsSuppressStatus(8) {
		t.Fatal("informatics: перебивает рамку только OK (0)")
	}
	for _, s := range []int{0, 8} {
		if !isInformaticsSolvedStatus(s) {
			t.Fatalf("informatics: %d должно быть решено", s)
		}
	}
	if isInformaticsSolvedStatus(16) {
		t.Fatal("informatics: 16 не засчитываем")
	}

	// ejudge
	if !isEjudgeBorderStatus(0) || isEjudgeBorderStatus(8) || isEjudgeBorderStatus(16) {
		t.Fatal("ejudge: рамка только у OK (0)")
	}
	for _, s := range []int{0, 8, 16} {
		if !isEjudgeSolvedStatus(s) {
			t.Fatalf("ejudge: %d должно быть решено", s)
		}
	}
	for _, s := range []int{5, 11, 7} {
		if isEjudgeSolvedStatus(s) {
			t.Fatalf("ejudge: %d не должно быть решено", s)
		}
	}
}

// Рамкой помечается вердикт, который поставил человек, а не выдала тестирующая
// система. Списки у двух систем разные: на kod-u судья доводит посылку до
// полного OK, на informatics — до «зачтено».
func TestManualVerdictBorders(t *testing.T) {
	cases := []struct {
		status            int
		name              string
		informatics, kodu bool
	}{
		{0, "OK", false, true}, // informatics: выдаёт судья; kod-u: ставит преподаватель
		{8, "зачтено", true, false},
		{16, "ожидает подтверждения", false, false},
		{10, "дисквалифицировано", true, true},
		{17, "отвергнуто", true, true},
		{23, "вызван на защиту", true, true},
		// Нарушение правил оформления ejudge выставляет сам — не рамка.
		{14, "нарушение оформления", false, false},
		// Автоматические вердикты тестирования.
		{1, "ошибка компиляции", false, false},
		{3, "превышено время", false, false},
		{5, "неверный ответ", false, false},
		{7, "частичное решение", false, false},
		{12, "превышена память", false, false},
	}
	for _, c := range cases {
		if got := isInformaticsBorderStatus(c.status); got != c.informatics {
			t.Errorf("informatics: статус %d (%s) → рамка %v, ожидалось %v", c.status, c.name, got, c.informatics)
		}
		if got := isEjudgeBorderStatus(c.status); got != c.kodu {
			t.Errorf("ejudge: статус %d (%s) → рамка %v, ожидалось %v", c.status, c.name, got, c.kodu)
		}
	}
	// «Проигнорировано» в ejudge — действие судьи, но на informatics он
	// проставляется автоматически пачками (в т.ч. на технических задачах),
	// поэтому там рамку не даёт.
	if !isEjudgeBorderStatus(9) {
		t.Error("ejudge: «проигнорировано» ставит судья — ожидалась рамка")
	}
	if isInformaticsBorderStatus(9) {
		t.Error("informatics: «проигнорировано» выставляется автоматически — рамки быть не должно")
	}
}

// Вердикт преподавателя помечается и тогда, когда задача не решена: «отвергнуто»
// или «дисквалифицировано» — это тоже его решение, а не результат тестов.
func TestManualVerdictOnUnsolvedTask(t *testing.T) {
	build := func(id int) string {
		return "https://informatics.msk.ru/mod/statements/view.php?chapterid=" + itoa(id) + "#1"
	}
	for _, c := range []struct {
		name     string
		status   int
		accepted bool
	}{
		{"отвергнуто", 17, true},
		{"дисквалифицировано", 10, true},
		{"неверный ответ", 5, false},
		{"нарушение оформления", 14, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			agg := map[string]informaticsTaskAggregate{}
			foldStoredInformaticsRuns([]informaticsStoredRun{{ID: 1, ProblemID: 100, Status: c.status}}, agg, build)
			res := aggregatesToTaskResults(agg)
			if len(res) != 1 {
				t.Fatalf("ожидался один результат: %+v", res)
			}
			if res[0].Solved {
				t.Errorf("задача не решена, а Solved=%v", res[0].Solved)
			}
			if res[0].Accepted != c.accepted {
				t.Errorf("Accepted=%v, ожидалось %v", res[0].Accepted, c.accepted)
			}
		})
	}
}
