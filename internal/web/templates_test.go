package web

import (
	"testing"

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

// upsolvingView: без дорешки → nil; с дорешкой считает оконные суммы, места и
// сортировку. IOI-tasks: три ученика, у одного всё в дорешку — в оконном виде
// он падает вниз, места пересчитываются.
func TestUpsolvingViewIOITasks(t *testing.T) {
	tasks := []domain.GeneratedTask{{Label: "A"}, {Label: "B"}}
	contest := domain.GeneratedContestStandings{
		ScoreSystem: domain.ScoreSystemIOI,
		ContestType: domain.ContestTypeTasks,
		Tasks:       tasks,
	}
	// Аня: A=100 в окне, B=100 в дорешке (main=nil→практика). Итого полн.=200, окно=100.
	anya := domain.GeneratedRow{
		PublicName: "Аня", Place: "1", TotalScore: 200, SolvedCount: 2,
		Statuses:       []string{"solved", "solved"},
		Scores:         []*int{ip(100), nil},
		PracticeScores: []*int{nil, ip(100)},
		Upsolved:       []bool{false, true},
	}
	// Боря: A=100, B=100 обе в окне. Полн.=200, окно=200 → в оконном виде он первый.
	borya := domain.GeneratedRow{
		PublicName: "Боря", Place: "1", TotalScore: 200, SolvedCount: 2,
		Statuses: []string{"solved", "solved"},
		Scores:   []*int{ip(100), ip(100)},
	}
	contest.Rows = []domain.GeneratedRow{anya, borya}

	uv := upsolvingView(contest)
	if uv == nil {
		t.Fatal("должна быть дорешка → не nil")
	}
	if len(uv.Rows) != 2 {
		t.Fatalf("строк: %d", len(uv.Rows))
	}
	// Оконный порядок: Боря (200) выше Ани (100).
	if uv.Rows[0].PublicName != "Боря" || uv.Rows[0].Count != 200 || uv.Rows[0].Place != "1" {
		t.Fatalf("первый оконный: %+v", uv.Rows[0])
	}
	if uv.Rows[1].PublicName != "Аня" || uv.Rows[1].Count != 100 || uv.Rows[1].Place != "2" {
		t.Fatalf("второй оконный: %+v", uv.Rows[1])
	}
	// Ячейка B у Ани в оконном виде — пустая (дорешка).
	anyaWin := uv.Rows[1]
	if anyaWin.Cells[0].Text != "100" || anyaWin.Cells[1].Text != "" {
		t.Fatalf("оконные ячейки Ани: %q %q", anyaWin.Cells[0].Text, anyaWin.Cells[1].Text)
	}
}

// Нет дорешки → кнопки нет (nil).
func TestUpsolvingViewNone(t *testing.T) {
	contest := domain.GeneratedContestStandings{
		ScoreSystem: domain.ScoreSystemEdu, ContestType: domain.ContestTypeTasks,
		Tasks: []domain.GeneratedTask{{Label: "A"}},
		Rows: []domain.GeneratedRow{
			{PublicName: "Аня", Statuses: []string{"solved"}, SolvedCount: 1},
		},
	}
	if upsolvingView(contest) != nil {
		t.Fatal("без дорешки должно быть nil")
	}
}

// edu-tasks: дорешанная задача не считается решённой в оконном виде.
func TestUpsolvingViewEdu(t *testing.T) {
	contest := domain.GeneratedContestStandings{
		ScoreSystem: domain.ScoreSystemEdu, ContestType: domain.ContestTypeTasks,
		Tasks: []domain.GeneratedTask{{Label: "A"}, {Label: "B"}},
		Rows: []domain.GeneratedRow{{
			PublicName: "Аня", Place: "1", SolvedCount: 2,
			Statuses: []string{"solved", "solved"},
			Upsolved: []bool{false, true}, // B решена только в дорешке
		}},
	}
	uv := upsolvingView(contest)
	if uv == nil || len(uv.Rows) != 1 {
		t.Fatalf("uv: %+v", uv)
	}
	r := uv.Rows[0]
	if r.Count != 1 { // в окне решена только A
		t.Fatalf("оконное «решено» = %d, ждём 1", r.Count)
	}
	if r.Cells[0].Text != "+" || r.Cells[0].Status != "solved" {
		t.Fatalf("A в окне должна быть '+': %+v", r.Cells[0])
	}
	if r.Cells[1].Text != "" || r.Cells[1].Status != "none" {
		t.Fatalf("B в окне должна быть пустой: %+v", r.Cells[1])
	}
}

// provider-контест (CF): место и порядок остаются по результату во время
// контеста, дорешка только убирает баллы из суммы и ячеек.
func TestUpsolvingViewProviderKeepsPlaces(t *testing.T) {
	contest := domain.GeneratedContestStandings{
		ScoreSystem: domain.ScoreSystemIOI, ContestType: domain.ContestTypeProvider,
		Tasks: []domain.GeneratedTask{{Label: "A"}},
		Rows: []domain.GeneratedRow{
			// Лидер по итогу за счёт дорешки, но место 1 — по контесту.
			{PublicName: "Аня", Place: "1", TotalScore: 100, SolvedCount: 1,
				Statuses: []string{"solved"}, Scores: []*int{ip(100)}, Upsolved: []bool{true}},
			{PublicName: "Боря", Place: "2", TotalScore: 50, SolvedCount: 0,
				Statuses: []string{"attempted"}, Scores: []*int{ip(50)}},
		},
	}
	uv := upsolvingView(contest)
	if uv == nil || len(uv.Rows) != 2 {
		t.Fatalf("uv: %+v", uv)
	}
	// Порядок и места — как были (по контесту), не пересортировка по окну.
	if uv.Rows[0].PublicName != "Аня" || uv.Rows[0].Place != "1" {
		t.Fatalf("место провайдера должно сохраниться: %+v", uv.Rows[0])
	}
	// Оконная сумма Ани — 0 (её задача дорешана: Scores у provider = балл дорешки).
	if uv.Rows[0].Count != 0 || uv.Rows[0].Cells[0].Text != "" {
		t.Fatalf("оконный балл Ани должен быть 0/пусто: count=%d cell=%q", uv.Rows[0].Count, uv.Rows[0].Cells[0].Text)
	}
	// Боря: 50 в контесте, дорешки нет → окно 50.
	if uv.Rows[1].Count != 50 {
		t.Fatalf("оконный балл Бори = %d, ждём 50", uv.Rows[1].Count)
	}
}
