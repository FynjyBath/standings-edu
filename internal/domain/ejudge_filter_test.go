package domain

import (
	"strings"
	"testing"
)

// Фильтр прогонов ejudge: поля login и prob, строки в кавычках, части
// объединяются через &&; пустые части опускаются.
func TestEjudgeRunFilter(t *testing.T) {
	cases := []struct {
		login, prob, want string
	}{
		{"ivanov", "A", `login == "ivanov" && prob == "A"`},
		{"ivanov", "", `login == "ivanov"`},
		{"", "A", `prob == "A"`},
		{"", "", ""},
		{"  ivanov  ", " A ", `login == "ivanov" && prob == "A"`},
		// Короткие имена в ejudge бывают не только буквой.
		{"user-1", "task_03", `login == "user-1" && prob == "task_03"`},
		// Кавычка в значении: двойные кавычки внутри двойных недопустимы,
		// берём в одинарные.
		{`iva"nov`, "A", `login == 'iva"nov' && prob == "A"`},
		// Оба вида кавычек литералом не записать — часть выпадает, но выражение
		// остаётся синтаксически верным.
		{`iva"n'ov`, "A", `prob == "A"`},
	}
	for _, c := range cases {
		if got := EjudgeRunFilter(c.login, c.prob); got != c.want {
			t.Errorf("EjudgeRunFilter(%q, %q) = %q, ожидалось %q", c.login, c.prob, got, c.want)
		}
	}
}

// Судейская ссылка отличается от клиентской — по ней страница понимает, что
// смотрит преподаватель, и показывает фильтр.
func TestIsEjudgeJudgeURL(t *testing.T) {
	judge := []string{
		"https://ej.example.org/new-judge?contest_id=5",
		"https://ej.example.org/cgi-bin/new-judge?contest_id=5",
		"https://ej.example.org/new-judge/?contest_id=5",
	}
	client := []string{
		"https://ej.example.org/new-client?contest_id=5&prob_id=3",
		"https://informatics.msk.ru/mod/statements/view.php?chapterid=1",
		"", "не ссылка",
	}
	for _, u := range judge {
		if !IsEjudgeJudgeURL(u) {
			t.Errorf("%q должна считаться судейской", u)
		}
	}
	for _, u := range client {
		if IsEjudgeJudgeURL(u) {
			t.Errorf("%q не должна считаться судейской", u)
		}
	}
}

// Судейская ссылка строится из клиентской и адресуется контестом: номер задачи
// в new-judge не передаётся — ради этого и нужен фильтр.
func TestEjudgeJudgeURLDropsProblem(t *testing.T) {
	got := EjudgeJudgeURL("https://ej.example.org/new-client?contest_id=42&prob_id=7")
	want := "https://ej.example.org/new-judge?contest_id=42"
	if got != want {
		t.Fatalf("EjudgeJudgeURL = %q, ожидалось %q", got, want)
	}
}

// Ячейка суммы в сводной ведёт на контест ejudge: при судейском доступе ссылка
// переводится в режим судьи, а отбор по ученику уходит в буфер строкой фильтра.
func TestSwapEjudgeLinksRewritesContestSourceURL(t *testing.T) {
	std := &GeneratedGroupStandings{
		Contests: []GeneratedContestStandings{{
			Title:            "Перебор",
			SummaryTotalOnly: true,
			SourceURL:        "https://ej.kod-u.ru/new-client?contest_id=933977",
			EjudgeSite:       "kodu_ejudge",
		}},
	}
	SwapEjudgeLinksToJudge(std)
	got := std.Contests[0].SourceURL
	if !IsEjudgeJudgeURL(got) {
		t.Fatalf("ссылка на контест должна вести в режим судьи, получили %q", got)
	}
	if !strings.Contains(got, "contest_id=933977") {
		t.Errorf("контест должен сохраниться: %q", got)
	}
}

// Без права на судейские ссылки логин ejudge не должен покидать сервер: вместе
// с ним снимается и сайт контеста, иначе по нему было бы что искать.
func TestStripEjudgeFilterDataClearsContestSite(t *testing.T) {
	std := &GeneratedGroupStandings{
		Contests: []GeneratedContestStandings{{
			Title:            "Перебор",
			SummaryTotalOnly: true,
			SourceURL:        "https://ej.kod-u.ru/new-client?contest_id=933977",
			EjudgeSite:       "kodu_ejudge",
			Rows: []GeneratedRow{{
				StudentID: "s1",
				Accounts:  map[string]string{"kodu_ejudge": "ivanov-i", "informatics": "12345"},
			}},
		}},
	}
	StripEjudgeFilterData(std)
	c := std.Contests[0]
	if c.EjudgeSite != "" {
		t.Errorf("сайт ejudge не должен уезжать в публичный вид: %q", c.EjudgeSite)
	}
	if _, leaked := c.Rows[0].Accounts["kodu_ejudge"]; leaked {
		t.Errorf("логин ejudge утёк: %+v", c.Rows[0].Accounts)
	}
	if c.Rows[0].Accounts["informatics"] != "12345" {
		t.Errorf("аккаунты других сайтов трогать не надо: %+v", c.Rows[0].Accounts)
	}
}

// Фильтр для ячейки суммы — только по ученику: в ней речь про весь контест,
// а не про отдельную задачу.
func TestEjudgeRunFilterStudentOnly(t *testing.T) {
	got := EjudgeRunFilter("ivanov-i", "")
	if got != `login == "ivanov-i"` {
		t.Fatalf("фильтр по одному ученику = %q", got)
	}
	if withProb := EjudgeRunFilter("ivanov-i", "A"); withProb == got {
		t.Error("фильтр с задачей должен отличаться от фильтра без неё")
	}
}
