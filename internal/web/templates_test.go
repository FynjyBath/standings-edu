package web

import (
	"testing"
	"time"

	"standings-edu/internal/domain"
)

func ip(v int) *int { return &v }

// Ячейки IOI: «50 (70)» — основной и дорешка; «(70)» — только дорешка;
// старые файлы без practice_scores — прежний вид (скобки по флагу).
func TestTaskCellsMainAndPractice(t *testing.T) {
	row := domain.GeneratedRow{
		Statuses:       []string{"solved", "solved", "solved", "attempted", "none"},
		Scores:         []*int{ip(50), nil, ip(100), ip(0), nil},
		PracticeScores: []*int{ip(70), ip(70), nil, nil, nil},
		Upsolved:       []bool{false, true, false, false, false},
	}
	cells := taskCells(row, domain.ScoreSystemIOI)
	want := []string{"50 (70)", "(70)", "100", "0", ""}
	for i, w := range want {
		if cells[i].Text != w {
			t.Fatalf("cell[%d]=%q want %q", i, cells[i].Text, w)
		}
	}
	// Фон — по лучшему баллу: у «50 (70)» альфа от 70.
	if cells[0].Alpha != scoreAlpha(ip(70)) {
		t.Fatalf("alpha must use best score: %v", cells[0].Alpha)
	}

	// Легаси-строка (нет practice_scores): дорешка в скобках по флагу, как раньше.
	legacy := domain.GeneratedRow{
		Statuses: []string{"solved"},
		Scores:   []*int{ip(70)},
		Upsolved: []bool{true},
	}
	cells = taskCells(legacy, domain.ScoreSystemIOI)
	if cells[0].Text != "(70)" {
		t.Fatalf("legacy cell=%q want (70)", cells[0].Text)
	}

	// edu не меняется.
	edu := domain.GeneratedRow{Statuses: []string{"solved"}, Upsolved: []bool{true}}
	cells = taskCells(edu, domain.ScoreSystemEdu)
	if cells[0].Text != "(+)" {
		t.Fatalf("edu cell=%q want (+)", cells[0].Text)
	}
}

// Подпись окна контеста: один день — компактно, разные дни — полный диапазон.
func TestContestWindowText(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	start := time.Date(2026, 7, 4, 18, 0, 0, 0, msk)
	endSameDay := time.Date(2026, 7, 4, 20, 0, 0, 0, msk)
	endNextDay := time.Date(2026, 7, 5, 20, 0, 0, 0, msk)

	if got := contestWindowText(&start, &endSameDay); got != "04.07.2026 18:00–20:00 MSK" {
		t.Fatalf("same day: %q", got)
	}
	if got := contestWindowText(&start, &endNextDay); got != "04.07.2026 18:00 — 05.07.2026 20:00 MSK" {
		t.Fatalf("cross day: %q", got)
	}
	if got := contestWindowText(&start, nil); got != "с 04.07.2026 18:00 MSK" {
		t.Fatalf("start only: %q", got)
	}
	if got := contestWindowText(nil, &endSameDay); got != "" {
		t.Fatalf("no start must be empty: %q", got)
	}
}
