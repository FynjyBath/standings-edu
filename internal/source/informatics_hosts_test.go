package source

import "testing"

// Ссылки на informatics принимаются одинаково с обоих зеркал (msk.ru и mccme.ru):
// сборник разворачивается (→ названия задач), отдельная задача матчится сайтом.
// Страница всегда качается с baseURL клиента, поэтому хост в ссылке на имена
// не влияет.
func TestInformaticsAcceptsBothMirrorHosts(t *testing.T) {
	statements := []string{
		"https://informatics.msk.ru/mod/statements/view.php?id=928",
		"https://informatics.mccme.ru/mod/statements/view.php?id=928",
		"https://www.informatics.mccme.ru/mod/statements/view.php?id=928",
	}
	for _, u := range statements {
		if id, ok := ParseInformaticsStatementID(u); !ok || id != 928 {
			t.Errorf("ParseInformaticsStatementID(%q) = %d, %v; want 928, true", u, id, ok)
		}
	}

	c, err := NewInformaticsAPIClientWithState(InformaticsCredentials{Username: "u", Password: "p"}, "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	for _, u := range statements {
		if !c.MatchTaskURL(u) {
			t.Errorf("MatchTaskURL(%q) = false; want true", u)
		}
	}

	// Отдельная задача сборника (chapterid) на mccme матчится сайтом (для
	// результатов), но сборником не считается (её имя мы не тянем — как и на msk).
	task := "https://informatics.mccme.ru/mod/statements/view.php?id=928&chapterid=111"
	if !c.MatchTaskURL(task) {
		t.Errorf("MatchTaskURL(%q) = false; want true", task)
	}
	if _, ok := ParseInformaticsStatementID(task); ok {
		t.Errorf("ParseInformaticsStatementID(%q) = true; ссылка на задачу не должна считаться сборником", task)
	}
}
