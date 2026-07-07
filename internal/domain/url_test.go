package domain

import "testing"

// Зеркала informatics сводятся к одному normalized_url — ссылки со старого
// домена mccme.ru должны матчиться с посылками (informatics.msk.ru).
func TestNormalizeTaskURLInformaticsMirrors(t *testing.T) {
	want := NormalizeTaskURL("https://informatics.msk.ru/mod/statements/view.php?chapterid=1209#1")
	if want == "" {
		t.Fatal("canonical normalization is empty")
	}
	mirrors := []string{
		"https://informatics.mccme.ru/mod/statements/view.php?chapterid=1209#1",
		"https://www.informatics.mccme.ru/mod/statements/view.php?chapterid=1209",
		"https://www.informatics.msk.ru/mod/statements/view.php?chapterid=1209#5",
	}
	for _, raw := range mirrors {
		if got := NormalizeTaskURL(raw); got != want {
			t.Fatalf("mirror %q normalized to %q, want %q", raw, got, want)
		}
	}

	// acmp канонизируется отдельным тестом ниже.
}

// Ссылки на задачу acmp.ru сводятся к одному normalized_url по id_task, как бы
// их ни записали: /index.asp против /, порядок query, регистр main, схема, www,
// фрагмент. Иначе задача, добавленная в контест «браузерной» ссылкой
// (…/index.asp?main=task&id_task=N), не совпадёт с решёнными задачами, которые
// клиент отдаёт как https://acmp.ru/?main=task&id_task=N.
func TestNormalizeTaskURLACMP(t *testing.T) {
	want := "https://acmp.ru/?main=task&id_task=1157"
	forms := []string{
		"https://acmp.ru/?main=task&id_task=1157",
		"https://acmp.ru/index.asp?main=task&id_task=1157",
		"http://acmp.ru/index.asp?main=Task&id_task=1157",
		"https://acmp.ru/index.asp?id_task=1157&main=task",
		"https://www.acmp.ru/index.asp?main=task&id_task=1157",
		"https://acmp.ru/index.asp?main=task&id_task=1157#top",
	}
	for _, raw := range forms {
		if got := NormalizeTaskURL(raw); got != want {
			t.Fatalf("acmp form %q normalized to %q, want %q", raw, got, want)
		}
	}

	// Страница пользователя (нет id_task) задачей не считается.
	user := "https://acmp.ru/index.asp?main=user&id=501370"
	if got := NormalizeTaskURL(user); got != user {
		t.Fatalf("acmp user page must stay intact: %q", got)
	}
}

// Ссылки на задачу informatics сводятся к одному normalized_url по chapterid:
// «браузерная» форма с id сборника, порядок query, зеркало mccme, www, фрагмент.
// Иначе задача, добавленная ссылкой …?id=2296&chapterid=2937, не совпала бы с
// посылками, которые строятся как …?chapterid=2937.
func TestNormalizeTaskURLInformaticsChapter(t *testing.T) {
	want := "https://informatics.msk.ru/mod/statements/view.php?chapterid=2937"
	forms := []string{
		"https://informatics.msk.ru/mod/statements/view.php?chapterid=2937",
		"https://informatics.msk.ru/mod/statements/view.php?chapterid=2937#1",
		"https://informatics.msk.ru/mod/statements/view.php?id=2296&chapterid=2937",
		"https://informatics.msk.ru/mod/statements/view.php?chapterid=2937&id=2296",
		"https://www.informatics.msk.ru/mod/statements/view.php?id=2296&chapterid=2937",
		"https://informatics.mccme.ru/mod/statements/view.php?id=2296&chapterid=2937#1",
	}
	for _, raw := range forms {
		if got := NormalizeTaskURL(raw); got != want {
			t.Fatalf("informatics form %q normalized to %q, want %q", raw, got, want)
		}
	}

	// Ссылка на сборник (только id, без chapterid) задачей не считается —
	// её разворачивает билд, а не нормализация.
	statement := "https://informatics.msk.ru/mod/statements/view.php?id=2296"
	if got := NormalizeTaskURL(statement); got != statement {
		t.Fatalf("informatics statement page must stay intact: %q", got)
	}
}
