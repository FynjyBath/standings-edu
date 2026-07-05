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

	// Чужие домены не трогаем.
	other := NormalizeTaskURL("https://acmp.ru/?main=task&id_task=29")
	if other != "https://acmp.ru/?main=task&id_task=29" {
		t.Fatalf("foreign domain must stay intact: %q", other)
	}
}
