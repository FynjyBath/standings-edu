package source

import "testing"

func TestParseInformaticsTaskTitle(t *testing.T) {
	cases := map[string]string{
		`<html><head><title>Задача №1209. Клавиатура</title></head>`: "Клавиатура",
		`<title>Задача №843. Простая последовательность</title>`:     "Простая последовательность",
		`<title>Задача № 2936.Гипотенуза</title>`:                    "Гипотенуза",
		`<title>Главная</title>`:                                     "",
	}
	for html, want := range cases {
		if got := parseInformaticsTaskTitle([]byte(html)); got != want {
			t.Errorf("parseInformaticsTaskTitle(%q) = %q, want %q", html, got, want)
		}
	}
}

func TestParseInformaticsTaskChapterID(t *testing.T) {
	ok := map[string]int{
		"https://informatics.msk.ru/mod/statements/view.php?chapterid=1209#1":     1209,
		"https://informatics.mccme.ru/mod/statements/view.php?id=5&chapterid=843": 843,
	}
	for u, want := range ok {
		if id, k := ParseInformaticsTaskChapterID(u); !k || id != want {
			t.Errorf("ParseInformaticsTaskChapterID(%q) = %d, %v; want %d, true", u, id, k, want)
		}
	}
	// Ссылка на сборник (без chapterid) — это не отдельная задача.
	if _, k := ParseInformaticsTaskChapterID("https://informatics.msk.ru/mod/statements/view.php?id=928"); k {
		t.Error("сборник не должен считаться отдельной задачей")
	}
}
