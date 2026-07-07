package source

import "testing"

func TestParseInformaticsStatementID(t *testing.T) {
	// Сборник: id без chapterid.
	cases := map[string]int{
		"https://informatics.msk.ru/mod/statements/view.php?id=2296":   2296,
		"https://informatics.msk.ru/mod/statements/view.php?id=2296#1": 2296,
		"https://www.informatics.msk.ru/mod/statements/view.php?id=99": 99,
		"https://informatics.mccme.ru/mod/statements/view.php?id=2296": 2296,
	}
	for raw, want := range cases {
		if got, ok := ParseInformaticsStatementID(raw); !ok || got != want {
			t.Errorf("%q -> (%d,%v), want (%d,true)", raw, got, ok, want)
		}
	}

	// Не сборник: есть chapterid (это задача), другой сайт/путь, нет id.
	notStatements := []string{
		"https://informatics.msk.ru/mod/statements/view.php?id=2296&chapterid=2937",
		"https://informatics.msk.ru/mod/statements/view.php?chapterid=2937",
		"https://informatics.msk.ru/user/profile.php?id=849280",
		"https://codeforces.com/contest/1711",
		"https://informatics.msk.ru/mod/statements/view.php",
	}
	for _, raw := range notStatements {
		if _, ok := ParseInformaticsStatementID(raw); ok {
			t.Errorf("%q must NOT be a statement", raw)
		}
	}
}

func TestParseInformaticsStatementProblems(t *testing.T) {
	// Мини-оглавление: активная задача A (без ссылки, chapterid из problem_data),
	// затем B и C ссылками; посторонний chapterid вне statements_toc игнорируется.
	html := []byte(`
<div id='problem_data' style='display:none' problem_id='2936' statement_id='928'></div>
<a href="view.php?id=2296&chapterid=2936">Посылки по задаче</a>
<div class="statements_toc statements_toc_alpha clearfix"><ul>
<li><strong class="text-truncate"><b>A.</b> Гипотенуза</strong></li>
<li><a title="X" href="view.php?id=2296&amp;chapterid=2937"><b>B.</b> Следующее</a></li>
<li><a title="Y" href="view.php?id=2296&amp;chapterid=2938"><b>C.</b> Дележ</a></li>
</ul></div>`)

	problems, err := parseInformaticsStatementProblems(html)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wantIDs := []int{2936, 2937, 2938}
	if len(problems) != len(wantIDs) {
		t.Fatalf("got %d problems, want %d: %+v", len(problems), len(wantIDs), problems)
	}
	for i, want := range wantIDs {
		if problems[i].ChapterID != want {
			t.Errorf("problem[%d] chapterid=%d, want %d", i, problems[i].ChapterID, want)
		}
	}
	if problems[0].Title != "A. Гипотенуза" {
		t.Errorf("active title = %q", problems[0].Title)
	}
	if problems[1].Title != "B. Следующее" {
		t.Errorf("linked title = %q", problems[1].Title)
	}
}
