package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

// newTestHandlers поднимает Handlers с временным data-каталогом и настроенной
// админкой. Auth-middleware в тестах не участвует — проверяем логику хендлеров.
func newTestHandlers(t *testing.T) (*Handlers, string) {
	t.Helper()
	dataDir := t.TempDir()
	h := NewHandlers(
		storage.NewGeneratedLoader(t.TempDir()),
		nil,
		web.NewTemplateRenderer(filepath.Join("..", "..", "web", "templates")),
		log.New(io.Discard, "", 0),
	)
	if err := h.ConfigureAdmin(AdminConfig{
		Login: "admin", Password: "pw",
		ProjectRoot: t.TempDir(), DataDir: dataDir,
	}); err != nil {
		t.Fatalf("ConfigureAdmin: %v", err)
	}
	return h, dataDir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setupGroup(t *testing.T, dataDir, slug, contestsJSON string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dataDir, "groups", slug, "group.json"),
		`{"title":"Тестовая","student_ids":["s1"]}`)
	if contestsJSON != "" {
		writeTestFile(t, filepath.Join(dataDir, "groups", slug, "contests.json"), contestsJSON)
	}
}

// postJSON вызывает хендлер с JSON-телом и разбирает ответ.
func postJSON(t *testing.T, handler http.HandlerFunc, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %v; body=%s", err, rec.Body.String())
	}
	return rec.Code, payload
}

func readGroupContestsRaw(t *testing.T, dataDir, slug string) []map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dataDir, "groups", slug, "contests.json"))
	if err != nil {
		t.Fatalf("read contests.json: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("parse contests.json: %v; body=%s", err, body)
	}
	return items
}

func TestAdminGroupContestAddRef(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "contests.json"),
		`[{"id":"g1","title":"Глобальный","score_system":"edu","subcontests":[]},{"id":"g2","title":"Второй","score_system":"edu","subcontests":[]}]`)
	setupGroup(t, dataDir, "grp", `[]`)

	code, resp := postJSON(t, h.AdminGroupContestAddRef, `{"slug":"grp","id":"g1"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("add-ref failed: code=%d resp=%v", code, resp)
	}
	items := readGroupContestsRaw(t, dataDir, "grp")
	if len(items) != 1 || items[0]["id"] != "g1" || items[0]["update"] != true {
		t.Fatalf("unexpected contests.json: %v", items)
	}

	// Следующий добавленный контест встаёт сверху списка.
	code, resp = postJSON(t, h.AdminGroupContestAddRef, `{"slug":"grp","id":"g2"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("second add-ref failed: code=%d resp=%v", code, resp)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	if len(items) != 2 || items[0]["id"] != "g2" || items[1]["id"] != "g1" {
		t.Fatalf("new contest must be prepended: %v", items)
	}

	// Повторное добавление — отказ.
	code, resp = postJSON(t, h.AdminGroupContestAddRef, `{"slug":"grp","id":"g1"}`)
	if code != http.StatusBadRequest || resp["ok"] != false {
		t.Fatalf("expected duplicate rejection, got code=%d resp=%v", code, resp)
	}

	// Несуществующий глобальный контест — отказ.
	code, _ = postJSON(t, h.AdminGroupContestAddRef, `{"slug":"grp","id":"nope"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected rejection for unknown contest, got code=%d", code)
	}

	// Несуществующая группа — отказ.
	code, _ = postJSON(t, h.AdminGroupContestAddRef, `{"slug":"missing","id":"g1"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected rejection for unknown group, got code=%d", code)
	}

	// Группа без contests.json: первый add-ref должен создать файл.
	setupGroup(t, dataDir, "fresh", "")
	code, resp = postJSON(t, h.AdminGroupContestAddRef, `{"slug":"fresh","id":"g1"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("add-ref to fresh group failed: code=%d resp=%v", code, resp)
	}
	items = readGroupContestsRaw(t, dataDir, "fresh")
	if len(items) != 1 || items[0]["id"] != "g1" {
		t.Fatalf("contests.json not created for fresh group: %v", items)
	}
}

func TestAdminGroupContestRemove(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	setupGroup(t, dataDir, "grp",
		`[{"id":"a","update":true},{"id":"b","title":"Inline","score_system":"edu","subcontests":[]}]`)

	code, resp := postJSON(t, h.AdminGroupContestRemove, `{"slug":"grp","id":"a"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("remove failed: code=%d resp=%v", code, resp)
	}
	items := readGroupContestsRaw(t, dataDir, "grp")
	if len(items) != 1 || items[0]["id"] != "b" {
		t.Fatalf("expected only inline b left, got %v", items)
	}

	// Удаление отсутствующего — отказ, файл не меняется.
	code, _ = postJSON(t, h.AdminGroupContestRemove, `{"slug":"grp","id":"a"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected rejection for missing contest, got code=%d", code)
	}
}

func TestAdminGroupContestSetOptionsRef(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	setupGroup(t, dataDir, "grp", `[{"id":"a","update":true}]`)

	code, resp := postJSON(t, h.AdminGroupContestSetOptions,
		`{"slug":"grp","id":"a","update":false,"table_name":"Тема1, Тема2"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set-options failed: code=%d resp=%v", code, resp)
	}
	items := readGroupContestsRaw(t, dataDir, "grp")
	if items[0]["update"] != false {
		t.Fatalf("update flag not saved: %v", items)
	}
	tn, ok := items[0]["table_name"].([]any)
	if !ok || len(tn) != 2 || tn[0] != "Тема1" || tn[1] != "Тема2" {
		t.Fatalf("table_name not saved as list: %v", items)
	}

	// Одиночное имя — строкой (обратная совместимость формата).
	postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"table_name":"Одна"}`)
	items = readGroupContestsRaw(t, dataDir, "grp")
	if items[0]["table_name"] != "Одна" {
		t.Fatalf("single table_name should be a string: %v", items)
	}

	// Окно контеста на стороне группы: пишется в запись ссылки.
	code, resp = postJSON(t, h.AdminGroupContestSetOptions,
		`{"slug":"grp","id":"a","update":true,"start_time":"2026-09-01T18:00:00+03:00","end_time":"2026-09-01T20:00:00+03:00"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set-options with window failed: code=%d resp=%v", code, resp)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	if items[0]["start_time"] != "2026-09-01T18:00:00+03:00" || items[0]["end_time"] != "2026-09-01T20:00:00+03:00" {
		t.Fatalf("window not saved: %v", items)
	}

	// Пустое окно — поля убираются из записи.
	postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"start_time":"","end_time":""}`)
	items = readGroupContestsRaw(t, dataDir, "grp")
	if _, has := items[0]["start_time"]; has {
		t.Fatalf("empty window must clear start_time: %v", items)
	}

	// Невалидный ISO — отказ.
	code, _ = postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"start_time":"завтра"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected bad start_time rejection, got code=%d", code)
	}

	// Заморозка: пишется в запись, очищается, невалидная — отказ.
	code, resp = postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"freeze":"1h30m"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set freeze failed: code=%d resp=%v", code, resp)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	if items[0]["freeze"] != "1h30m" {
		t.Fatalf("freeze not saved: %v", items)
	}
	postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"freeze":""}`)
	items = readGroupContestsRaw(t, dataDir, "grp")
	if _, has := items[0]["freeze"]; has {
		t.Fatalf("empty freeze must clear the field: %v", items)
	}
	code, _ = postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"freeze":"полчаса"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected bad freeze rejection, got code=%d", code)
	}
}

// Заморозка у inline-контеста: ставится через set-options (entry-поле) и
// переживает редактирование тела через inline-save.
func TestAdminGroupContestFreezeInline(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	setupGroup(t, dataDir, "grp",
		`[{"id":"inl","title":"Инлайн","score_system":"edu","subcontests":[],"custom_field":"keep"}]`)

	code, resp := postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"inl","update":true,"freeze":"all"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set freeze on inline failed: code=%d resp=%v", code, resp)
	}
	items := readGroupContestsRaw(t, dataDir, "grp")
	if items[0]["freeze"] != "all" || items[0]["custom_field"] != "keep" {
		t.Fatalf("inline freeze not saved or body damaged: %v", items)
	}

	// Редактирование тела не должно стирать заморозку.
	code, _ = postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","original_id":"inl","id":"inl","title":"Инлайн v2","score_system":"edu","source_type":"tasks","subcontests":[]}`)
	if code != http.StatusOK {
		t.Fatalf("inline edit failed: code=%d", code)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	if items[0]["title"] != "Инлайн v2" || items[0]["freeze"] != "all" {
		t.Fatalf("freeze lost on inline edit: %v", items)
	}

	// Снятие заморозки с inline.
	postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"inl","update":true,"freeze":""}`)
	items = readGroupContestsRaw(t, dataDir, "grp")
	if _, has := items[0]["freeze"]; has {
		t.Fatalf("freeze must be removed from inline: %v", items)
	}
}

func TestAdminGroupContestSetOptionsInlinePreservesBody(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	// Inline с неизвестным полем: оно должно пережить смену update.
	setupGroup(t, dataDir, "grp",
		`[{"id":"inl","title":"Inline","score_system":"edu","subcontests":[],"custom_field":"keep-me"}]`)

	code, resp := postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"inl","update":false,"table_name":"игнор"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set-options inline failed: code=%d resp=%v", code, resp)
	}
	items := readGroupContestsRaw(t, dataDir, "grp")
	e := items[0]
	if e["update"] != false || e["title"] != "Inline" || e["custom_field"] != "keep-me" {
		t.Fatalf("inline body not preserved: %v", e)
	}
	if _, has := e["table_name"]; has {
		t.Fatalf("table_name must not be written into inline entry: %v", e)
	}
}

func TestAdminGroupContestInlineSave(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "contests.json"), `[]`)
	setupGroup(t, dataDir, "grp", `[{"id":"ref1","update":true}]`)

	// Создание: новый контест встаёт в начало списка.
	code, resp := postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","id":"inl1","title":"Новый","score_system":"edu","source_type":"tasks","subcontests":[{"title":"S","tasks":["https://acmp.ru/?main=task&id_task=1"]}]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("inline create failed: code=%d resp=%v", code, resp)
	}
	items := readGroupContestsRaw(t, dataDir, "grp")
	if len(items) != 2 || items[0]["id"] != "inl1" || items[0]["update"] != true || items[0]["title"] != "Новый" {
		t.Fatalf("inline not prepended: %v", items)
	}
	if items[1]["id"] != "ref1" {
		t.Fatalf("existing entry must stay after new one: %v", items)
	}

	// id, совпадающий с существующей ссылкой — отказ.
	code, _ = postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","id":"ref1","title":"Дубль","score_system":"edu","source_type":"tasks","subcontests":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected duplicate id rejection, got code=%d", code)
	}

	// Выключаем update, затем правим тело без поля update — флаг должен сохраниться.
	postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"inl1","update":false}`)
	code, resp = postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","original_id":"inl1","id":"inl1","title":"Правка","score_system":"ioi","source_type":"tasks","subcontests":[]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("inline edit failed: code=%d resp=%v", code, resp)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	e := items[0]
	if e["title"] != "Правка" || e["score_system"] != "ioi" || e["update"] != false {
		t.Fatalf("inline edit lost fields or update flag: %v", e)
	}

	// Переименование id через original_id: позиция записи сохраняется.
	code, _ = postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","original_id":"inl1","id":"inl2","title":"Правка","score_system":"ioi","source_type":"tasks","subcontests":[]}`)
	if code != http.StatusOK {
		t.Fatalf("inline rename failed: code=%d", code)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	if len(items) != 2 || items[0]["id"] != "inl2" {
		t.Fatalf("rename did not replace entry in place: %v", items)
	}

	// Невалидный provider_config — отказ.
	code, _ = postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","id":"p1","source_type":"provider","provider":"codeforces_contest","provider_config":"{oops"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected provider_config rejection, got code=%d", code)
	}

	// Provider-контест с валидным конфигом — тоже встаёт в начало.
	code, _ = postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","id":"p1","source_type":"provider","provider":"codeforces_contest","provider_config":"{\"contest_id\":1711}"}`)
	if code != http.StatusOK {
		t.Fatalf("provider inline create failed: code=%d", code)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	first := items[0]
	if first["id"] != "p1" || first["source_type"] != "provider" || first["provider"] != "codeforces_contest" {
		t.Fatalf("provider inline fields missing or not prepended: %v", first)
	}
}

func TestGroupContestWritesPreserveBrokenEntries(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	// Не-объектная запись (битая для генератора) не должна молча удаляться.
	setupGroup(t, dataDir, "grp", `["broken-string-entry",{"id":"a","update":true}]`)

	code, resp := postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":false}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set-options failed: code=%d resp=%v", code, resp)
	}
	body, err := os.ReadFile(filepath.Join(dataDir, "groups", "grp", "contests.json"))
	if err != nil {
		t.Fatalf("read contests.json: %v", err)
	}
	if !strings.Contains(string(body), `"broken-string-entry"`) {
		t.Fatalf("broken entry was silently dropped: %s", body)
	}
}

func TestAdminGroupManagePageRenders(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "contests.json"),
		`[{"id":"g1","title":"Глобальный","score_system":"edu","subcontests":[]}]`)
	setupGroup(t, dataDir, "grp",
		`[{"id":"g1","update":true},{"id":"inl","title":"Инлайн <б>","score_system":"edu","subcontests":[]}]`)
	writeTestFile(t, filepath.Join(dataDir, "students.json"),
		`[{"id":"s1","full_name":"Иван Иванов","public_name":"Иван И.","accounts":[]}]`)

	req := httptest.NewRequest(http.MethodGet, "/standings/admin/group?slug=grp", nil)
	rec := httptest.NewRecorder()
	h.AdminGroupManagePage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("page status=%d body=%s", rec.Code, rec.Body.String())
	}
	html := rec.Body.String()
	for _, want := range []string{"g1", "inl", "inline-data", "add-ref-select"} {
		if !strings.Contains(html, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	// Регрессия: html/template в JS-контексте сам оборачивает строку в кавычки;
	// printf "%q" давал var slug = "\"grp\"" и сервер отвечал "group not found".
	if !strings.Contains(html, `var slug = "grp";`) {
		t.Fatalf("slug embedded incorrectly in page JS")
	}

	// Встроенный JSON inline-контестов должен корректно парситься.
	start := strings.Index(html, `<script id="inline-data" type="application/json">`)
	if start < 0 {
		t.Fatal("inline-data script not found")
	}
	rest := html[start+len(`<script id="inline-data" type="application/json">`):]
	end := strings.Index(rest, "</script>")
	var inline map[string]map[string]any
	if err := json.Unmarshal([]byte(rest[:end]), &inline); err != nil {
		t.Fatalf("inline-data is not valid JSON: %v; blob=%s", err, rest[:end])
	}
	if inline["inl"]["title"] != "Инлайн <б>" {
		t.Fatalf("inline title mismatch: %v", inline)
	}
}

func TestAdminStudentSaveAndDelete(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	setupGroup(t, dataDir, "grp", "")
	writeTestFile(t, filepath.Join(dataDir, "groups", "grp", "group.json"),
		`{"title":"Т","student_ids":["ivanov-ivan"]}`)

	// Создание.
	code, resp := postJSON(t, h.AdminStudentSave,
		`{"full_name":"Иванов Иван","accounts":[{"site":"codeforces","account_id":"ivan"}]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("student create failed: code=%d resp=%v", code, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("no id returned: %v", resp)
	}

	// Редактирование по id.
	code, _ = postJSON(t, h.AdminStudentSave,
		`{"id":"`+id+`","full_name":"Иванов Иван","public_name":"Ваня","accounts":[]}`)
	if code != http.StatusOK {
		t.Fatalf("student edit failed: code=%d", code)
	}

	// Удаление: должен исчезнуть и из student_ids группы.
	writeTestFile(t, filepath.Join(dataDir, "groups", "grp", "group.json"),
		`{"title":"Т","student_ids":["`+id+`","other"]}`)
	code, _ = postJSON(t, h.AdminStudentDelete, `{"id":"`+id+`"}`)
	if code != http.StatusOK {
		t.Fatalf("student delete failed: code=%d", code)
	}
	groupBody, _ := os.ReadFile(filepath.Join(dataDir, "groups", "grp", "group.json"))
	if strings.Contains(string(groupBody), id) {
		t.Fatalf("student id not removed from group: %s", groupBody)
	}
	if !strings.Contains(string(groupBody), "other") {
		t.Fatalf("other member must stay: %s", groupBody)
	}
}

func TestAdminContestSaveAndDelete(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "contests.json"), `[]`)

	code, resp := postJSON(t, h.AdminContestSave,
		`{"id":"c1","title":"К1","score_system":"edu","source_type":"tasks","table_name":"Тема","start_time":"2026-09-01T18:00:00+03:00","subcontests":[{"title":"S","tasks":["https://codeforces.com/problemset/problem/1/A"]}]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("contest create failed: code=%d resp=%v", code, resp)
	}

	// Дубликат id — отказ.
	code, _ = postJSON(t, h.AdminContestSave,
		`{"id":"c1","score_system":"edu","source_type":"tasks","subcontests":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected duplicate contest rejection, got code=%d", code)
	}

	// Кривой start_time — отказ.
	code, _ = postJSON(t, h.AdminContestSave,
		`{"id":"c2","score_system":"edu","source_type":"tasks","start_time":"завтра","subcontests":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected bad start_time rejection, got code=%d", code)
	}

	code, _ = postJSON(t, h.AdminContestDelete, `{"id":"c1"}`)
	if code != http.StatusOK {
		t.Fatalf("contest delete failed: code=%d", code)
	}
	body, _ := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if strings.Contains(string(body), "c1") {
		t.Fatalf("contest not deleted: %s", body)
	}
}

// До начала контеста ссылки на задачи вычищаются из отдаваемых standings
// (страницы, сводная, API); после начала — остаются.
func TestHideUpcomingContestTaskURLs(t *testing.T) {
	future := time.Now().Add(2 * time.Hour)
	past := time.Now().Add(-2 * time.Hour)
	standings := domain.GeneratedGroupStandings{
		Contests: []domain.GeneratedContestStandings{
			{
				ID: "upcoming", StartTime: &future,
				Tasks:       []domain.GeneratedTask{{Label: "A", URL: "https://acmp.ru/?id=1", NormalizedURL: "acmp.ru/?id=1"}},
				Subcontests: []domain.GeneratedSubcontest{{Tasks: []domain.GeneratedTask{{Label: "A", URL: "https://acmp.ru/?id=1"}}}},
			},
			{
				ID: "running", StartTime: &past,
				Tasks: []domain.GeneratedTask{{Label: "B", URL: "https://acmp.ru/?id=2"}},
			},
			{
				ID:    "no-window",
				Tasks: []domain.GeneratedTask{{Label: "C", URL: "https://acmp.ru/?id=3"}},
			},
		},
	}
	hideUpcomingContestTaskURLs(&standings)

	up := standings.Contests[0]
	if up.Tasks[0].URL != "" || up.Tasks[0].NormalizedURL != "" || up.Subcontests[0].Tasks[0].URL != "" {
		t.Fatalf("upcoming contest URLs must be hidden: %+v", up)
	}
	if up.Tasks[0].Label != "A" {
		t.Fatalf("labels must stay: %+v", up.Tasks)
	}
	if standings.Contests[1].Tasks[0].URL == "" {
		t.Fatalf("running contest URLs must stay: %+v", standings.Contests[1])
	}
	if standings.Contests[2].Tasks[0].URL == "" {
		t.Fatalf("no-window contest URLs must stay: %+v", standings.Contests[2])
	}
}

func TestAdminGroupMemberRemove(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	setupGroup(t, dataDir, "grp", "")
	writeTestFile(t, filepath.Join(dataDir, "groups", "grp", "group.json"),
		`{"title":"Т","student_ids":["s1","s2"]}`)

	code, resp := postJSON(t, h.AdminGroupMemberRemove, `{"slug":"grp","student_id":"s1"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("member remove failed: code=%d resp=%v", code, resp)
	}
	body, _ := os.ReadFile(filepath.Join(dataDir, "groups", "grp", "group.json"))
	if strings.Contains(string(body), "s1") || !strings.Contains(string(body), "s2") {
		t.Fatalf("unexpected group.json after removal: %s", body)
	}
}
