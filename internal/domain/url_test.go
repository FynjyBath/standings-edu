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

// Видимые informatics-ссылки переписываются под хост из base_url: msk→mccme и
// наоборот, независимо от того, как их вставили; фрагмент/query сохраняются.
func TestRewriteInformaticsHost(t *testing.T) {
	// base_url = mccme → любые informatics-ссылки на mccme.
	got := RewriteInformaticsHost("https://informatics.msk.ru/mod/statements/view.php?chapterid=5#2", "https://informatics.mccme.ru")
	if got != "https://informatics.mccme.ru/mod/statements/view.php?chapterid=5#2" {
		t.Fatalf("msk→mccme: %q", got)
	}
	// base_url = msk → на msk (в т.ч. www.mccme).
	got = RewriteInformaticsHost("https://www.informatics.mccme.ru/mod/statements/view.php?chapterid=7#1", "https://informatics.msk.ru")
	if got != "https://informatics.msk.ru/mod/statements/view.php?chapterid=7#1" {
		t.Fatalf("mccme→msk: %q", got)
	}
	// Не-informatics ссылку не трогаем.
	cf := "https://codeforces.com/problemset/problem/1/A"
	if got := RewriteInformaticsHost(cf, "https://informatics.msk.ru"); got != cf {
		t.Fatalf("non-informatics must be unchanged: %q", got)
	}
	// Пустой/битый base_url — без изменений.
	inf := "https://informatics.mccme.ru/x?y=1#1"
	if got := RewriteInformaticsHost(inf, ""); got != inf {
		t.Fatalf("empty base → unchanged: %q", got)
	}
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

// Ссылки ejudge (new-client) сводятся к contest_id (+ prob_id) на своём хосте:
// схема https, лишние параметры (SID, action, locale) и фрагмент отбрасываются.
func TestNormalizeTaskURLEjudge(t *testing.T) {
	prob := "https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3"
	probForms := []string{
		prob,
		"http://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3",
		"https://ej.kod-u.ru/new-client?SID=deadbeef&contest_id=25408&prob_id=3&action=139",
		"https://ej.kod-u.ru/new-client?prob_id=3&contest_id=25408#top",
	}
	for _, raw := range probForms {
		if got := NormalizeTaskURL(raw); got != prob {
			t.Fatalf("ejudge problem %q normalized to %q, want %q", raw, got, prob)
		}
	}

	// Ссылка на контест (без prob_id) остаётся ссылкой на контест (её разворачивает
	// билд), а хост различает экземпляры.
	contest := "https://ej.kod-u.ru/new-client?contest_id=25408"
	if got := NormalizeTaskURL("https://ej.kod-u.ru/new-client?contest_id=25408&SID=x"); got != contest {
		t.Fatalf("ejudge contest url: got %q, want %q", got, contest)
	}
	other := "https://ej.example.org/new-client?contest_id=25408&prob_id=3"
	if NormalizeTaskURL(other) == prob {
		t.Fatal("different ejudge hosts must not collide")
	}
}
