package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Добавляемые informatics-ссылки сразу приводятся к зеркалу из base_url кредов:
// вводить можно любой домен (msk/mccme/www/http), в данных окажется настроенный.
func TestInformaticsLinksRewrittenOnSave(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	h.ConfigureInformaticsBaseURL("https://informatics.mccme.ru")

	// 1. Глобальный контест: задачи, материалы и provider_config.
	code, resp := postJSON(t, h.AdminContestSave, `{
		"id":"cmirror","title":"Зеркало","score_system":"edu",
		"materials":[{"title":"Разбор","url":"http://www.informatics.msk.ru/course/view.php?id=7"}],
		"subcontests":[{"title":"Задачи","tasks":[
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=111#1",
			"https://WWW.Informatics.MSK.ru/mod/statements/view.php?id=2296",
			"https://acmp.ru/?main=task&id_task=1"
		]}]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("save contest: %d %v", code, resp)
	}
	blob, _ := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	body := string(blob)
	if strings.Contains(body, "informatics.msk.ru") {
		t.Fatalf("в данных не должно остаться msk-ссылок:\n%s", body)
	}
	for _, want := range []string{
		"https://informatics.mccme.ru/mod/statements/view.php?chapterid=111#1",
		"https://informatics.mccme.ru/mod/statements/view.php?id=2296",
		"https://informatics.mccme.ru/course/view.php?id=7", // материал
		`https://acmp.ru/?main=task\u0026id_task=1`,         // чужой сайт не трогаем
	} {
		if !strings.Contains(body, want) {
			t.Errorf("нет ожидаемой ссылки %q:\n%s", want, body)
		}
	}

	// 2. Inline-контест группы (та же воронка — админка и панель).
	code, resp = postJSON(t, h.AdminGroupContestInlineSave, `{
		"slug":"g1","id":"inl-mirror","title":"Свой","score_system":"edu",
		"subcontests":[{"title":"З","tasks":["https://informatics.msk.ru/mod/statements/view.php?chapterid=222"]}]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("save inline: %d %v", code, resp)
	}
	blob, _ = os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	if !strings.Contains(string(blob), "informatics.mccme.ru/mod/statements/view.php?chapterid=222") {
		t.Fatalf("inline-ссылка должна быть переписана:\n%s", blob)
	}

	// 3. Обратное направление: зеркало msk — ссылки mccme приводятся к msk.
	h.ConfigureInformaticsBaseURL("https://informatics.msk.ru")
	code, _ = postJSON(t, h.AdminContestSave, `{
		"id":"cback","title":"Обратно","score_system":"edu",
		"subcontests":[{"title":"З","tasks":["https://informatics.mccme.ru/mod/statements/view.php?chapterid=333"]}]}`)
	if code != http.StatusOK {
		t.Fatalf("save back: %d", code)
	}
	blob, _ = os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if !strings.Contains(string(blob), "https://informatics.msk.ru/mod/statements/view.php?chapterid=333") {
		t.Fatalf("mccme→msk не сработало:\n%s", blob)
	}

	// 4. Зеркало не настроено — ссылки сохраняются как введены.
	h.ConfigureInformaticsBaseURL("")
	code, _ = postJSON(t, h.AdminContestSave, `{
		"id":"casis","title":"Как есть","score_system":"edu",
		"subcontests":[{"title":"З","tasks":["https://informatics.mccme.ru/mod/statements/view.php?chapterid=444"]}]}`)
	if code != http.StatusOK {
		t.Fatalf("save as-is: %d", code)
	}
	blob, _ = os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if !strings.Contains(string(blob), "https://informatics.mccme.ru/mod/statements/view.php?chapterid=444") {
		t.Fatalf("без base_url ссылка должна остаться исходной:\n%s", blob)
	}
}

// Ссылки в provider_config (moodle/html-таблица) тоже приводятся к зеркалу.
func TestInformaticsMirrorInProviderConfig(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	h.ConfigureInformaticsBaseURL("https://informatics.mccme.ru")

	code, resp := postJSON(t, h.AdminContestSave, `{
		"id":"cprov","title":"Импорт","score_system":"ioi","source_type":"provider",
		"provider":"html_table_import",
		"provider_config":"{\"url\":\"https://informatics.msk.ru/mod/statements/view.php?id=99\"}"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("save provider: %d %v", code, resp)
	}
	blob, _ := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if !strings.Contains(string(blob), "informatics.mccme.ru/mod/statements/view.php?id=99") {
		t.Fatalf("ссылка в provider_config должна быть переписана:\n%s", blob)
	}
	// JSON конфига остаётся валидным.
	var list []map[string]any
	if err := json.Unmarshal(blob, &list); err != nil {
		t.Fatalf("contests.json должен остаться валидным: %v", err)
	}
}

// Сырой редактор файлов: сохранённый JSON тоже приводится к зеркалу.
func TestInformaticsMirrorInRawFileSave(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	h.ConfigureInformaticsBaseURL("https://informatics.mccme.ru")

	content := `[{"id":"raw1","title":"Сырой","score_system":"edu","subcontests":[{"title":"З","tasks":["https://informatics.msk.ru/mod/statements/view.php?chapterid=555"]}]}]`
	code, resp := postJSON(t, h.AdminFileSave, `{"path":"data/contests.json","content":`+mustJSONString(content)+`}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("file save: %d %v", code, resp)
	}
	blob, _ := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if !strings.Contains(string(blob), "informatics.mccme.ru/mod/statements/view.php?chapterid=555") {
		t.Fatalf("ссылка в сыром файле должна быть переписана:\n%s", blob)
	}
}

func mustJSONString(s string) string {
	blob, _ := json.Marshal(s)
	return string(blob)
}
