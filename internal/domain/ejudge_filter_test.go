package domain

import "testing"

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
