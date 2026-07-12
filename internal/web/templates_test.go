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
	cells := taskCells(domain.GeneratedContestStandings{ScoreSystem: domain.ScoreSystemIOI}, row)
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
	cells = taskCells(domain.GeneratedContestStandings{ScoreSystem: domain.ScoreSystemIOI}, legacy)
	if cells[0].Text != "(70)" {
		t.Fatalf("legacy cell=%q want (70)", cells[0].Text)
	}

	// edu не меняется.
	edu := domain.GeneratedRow{Statuses: []string{"solved"}, Upsolved: []bool{true}}
	cells = taskCells(domain.GeneratedContestStandings{ScoreSystem: domain.ScoreSystemEdu}, edu)
	if cells[0].Text != "(+)" {
		t.Fatalf("edu cell=%q want (+)", cells[0].Text)
	}
}

func TestSubmissionURL(t *testing.T) {
	acc := map[string]string{"informatics": "764934"}
	// informatics: добавляет submit+user_id, фрагмент сохраняется.
	got := submissionURL("https://informatics.msk.ru/mod/statements/view.php?chapterid=2793#1", acc)
	want := "https://informatics.msk.ru/mod/statements/view.php?chapterid=2793&submit&user_id=764934#1"
	if got != want {
		t.Fatalf("informatics submission url:\n got %q\nwant %q", got, want)
	}
	// Зеркало mccme тоже.
	if got := submissionURL("https://informatics.mccme.ru/mod/statements/view.php?chapterid=5#2", acc); got == "" {
		t.Fatal("mccme mirror must be supported")
	}
	// Нет informatics-аккаунта → пусто.
	if got := submissionURL("https://informatics.msk.ru/mod/statements/view.php?chapterid=1#1", map[string]string{"codeforces": "x"}); got != "" {
		t.Fatalf("no informatics account → empty, got %q", got)
	}
	// Другой сайт → пусто (пока не поддерживаем).
	if got := submissionURL("https://codeforces.com/problemset/problem/1/A", acc); got != "" {
		t.Fatalf("codeforces not supported → empty, got %q", got)
	}
	// Пустой URL / нет аккаунтов.
	if submissionURL("", acc) != "" || submissionURL("https://informatics.msk.ru/x#1", nil) != "" {
		t.Fatal("empty url / no accounts → empty")
	}
}

// В ячейке с посылкой (solved/attempted) проставляется ссылка; в пустой — нет.
func TestTaskCellsSubmissionLink(t *testing.T) {
	contest := domain.GeneratedContestStandings{
		ScoreSystem: domain.ScoreSystemEdu,
		Tasks: []domain.GeneratedTask{
			{URL: "https://informatics.msk.ru/mod/statements/view.php?chapterid=10#1"},
			{URL: "https://informatics.msk.ru/mod/statements/view.php?chapterid=10#2"},
			{URL: "https://informatics.msk.ru/mod/statements/view.php?chapterid=10#3"},
		},
	}
	row := domain.GeneratedRow{
		Statuses: []string{"solved", "attempted", "none"},
		Accounts: map[string]string{"informatics": "42"},
	}
	cells := taskCells(contest, row)
	if cells[0].SubmissionURL == "" || cells[1].SubmissionURL == "" {
		t.Fatalf("solved/attempted должны быть ссылками: %+v", cells)
	}
	if cells[2].SubmissionURL != "" {
		t.Fatalf("пустая ячейка без ссылки: %q", cells[2].SubmissionURL)
	}
	// Без informatics-аккаунта ссылок нет.
	noAcc := taskCells(contest, domain.GeneratedRow{Statuses: []string{"solved", "attempted", "none"}})
	for _, c := range noAcc {
		if c.SubmissionURL != "" {
			t.Fatalf("без аккаунта ссылок быть не должно: %q", c.SubmissionURL)
		}
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

func TestSubmissionLink(t *testing.T) {
	accs := []domain.Account{
		{Site: "codeforces", AccountID: "tourist"},
		{Site: "informatics", AccountID: "849280"},
	}
	got := submissionLink("https://informatics.msk.ru/mod/statements/view.php?chapterid=160#1", accs)
	want := "https://informatics.msk.ru/mod/statements/view.php?chapterid=160&submit&user_id=849280#1"
	if got != want {
		t.Fatalf("informatics link:\n got %q\nwant %q", got, want)
	}
	// Другой сайт → пусто (fallback на задачу делает шаблон).
	if got := submissionLink("https://codeforces.com/problemset/problem/1/A", accs); got != "" {
		t.Fatalf("codeforces → empty, got %q", got)
	}
	// Нет informatics-аккаунта → пусто.
	if got := submissionLink("https://informatics.msk.ru/x#1", []domain.Account{{Site: "acmp", AccountID: "1"}}); got != "" {
		t.Fatalf("no informatics acc → empty, got %q", got)
	}
}
