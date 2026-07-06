package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	// Переопределения zero_penalty / summary_total_only: заданы явно.
	code, resp = postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"zero_penalty":0,"summary_total_only":false}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set overrides failed: code=%d resp=%v", code, resp)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	if v, ok := items[0]["zero_penalty"].(float64); !ok || v != 0 {
		t.Fatalf("zero_penalty=0 must be written explicitly: %v", items)
	}
	if v, ok := items[0]["summary_total_only"].(bool); !ok || v != false {
		t.Fatalf("summary_total_only=false must be written explicitly: %v", items)
	}
	// Отсутствие полей (null) — наследование: ключи убираются.
	postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true}`)
	items = readGroupContestsRaw(t, dataDir, "grp")
	if _, has := items[0]["zero_penalty"]; has {
		t.Fatalf("null zero_penalty must clear the field: %v", items)
	}
	if _, has := items[0]["summary_total_only"]; has {
		t.Fatalf("null summary_total_only must clear the field: %v", items)
	}
	// Отрицательный штраф — отказ.
	code, _ = postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"a","update":true,"zero_penalty":-1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("negative zero_penalty override must be rejected, got code=%d", code)
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

	// Заморозка inline живёт в теле контеста: форма присылает её вместе с
	// остальными полями (пустая — убрать).
	code, _ = postJSON(t, h.AdminGroupContestInlineSave,
		`{"slug":"grp","original_id":"inl","id":"inl","title":"Инлайн v2","score_system":"edu","source_type":"tasks","freeze":"all","subcontests":[]}`)
	if code != http.StatusOK {
		t.Fatalf("inline edit failed: code=%d", code)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	if items[0]["title"] != "Инлайн v2" || items[0]["freeze"] != "all" {
		t.Fatalf("freeze from form body lost: %v", items)
	}

	// Снятие заморозки с inline.
	postJSON(t, h.AdminGroupContestSetOptions, `{"slug":"grp","id":"inl","update":true,"freeze":""}`)
	items = readGroupContestsRaw(t, dataDir, "grp")
	if _, has := items[0]["freeze"]; has {
		t.Fatalf("freeze must be removed from inline: %v", items)
	}
}

func TestAdminGroupContestSetOptionsInlineUnified(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	// Inline с неизвестным полем: тело переживает правку entry-настроек,
	// а table_name и окно правятся из строки таблицы так же, как у ссылок.
	setupGroup(t, dataDir, "grp",
		`[{"id":"inl","title":"Inline","score_system":"edu","subcontests":[],"custom_field":"keep-me"}]`)

	code, resp := postJSON(t, h.AdminGroupContestSetOptions,
		`{"slug":"grp","id":"inl","update":false,"table_name":"Тема","start_time":"2026-09-01T18:00:00+03:00","end_time":"2026-09-01T20:00:00+03:00","freeze":"1h"}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set-options inline failed: code=%d resp=%v", code, resp)
	}
	items := readGroupContestsRaw(t, dataDir, "grp")
	e := items[0]
	if e["update"] != false || e["title"] != "Inline" || e["custom_field"] != "keep-me" {
		t.Fatalf("inline body not preserved: %v", e)
	}
	if e["table_name"] != "Тема" || e["start_time"] != "2026-09-01T18:00:00+03:00" || e["freeze"] != "1h" {
		t.Fatalf("inline entry settings not applied: %v", e)
	}

	// Очистка полей убирает ключи из объекта.
	code, _ = postJSON(t, h.AdminGroupContestSetOptions,
		`{"slug":"grp","id":"inl","update":true,"table_name":"","start_time":"","end_time":"","freeze":""}`)
	if code != http.StatusOK {
		t.Fatalf("clear inline settings failed: code=%d", code)
	}
	items = readGroupContestsRaw(t, dataDir, "grp")
	e = items[0]
	for _, key := range []string{"table_name", "start_time", "end_time", "freeze"} {
		if _, has := e[key]; has {
			t.Fatalf("key %q must be removed: %v", key, e)
		}
	}
	if e["title"] != "Inline" || e["custom_field"] != "keep-me" {
		t.Fatalf("inline body damaged on clear: %v", e)
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

	// zero_penalty, summary_total_only и short_name сохраняются в контест;
	// отрицательный штраф — отказ.
	code, _ = postJSON(t, h.AdminContestSave,
		`{"original_id":"c1","id":"c1","title":"К1","short_name":"Ол-1","score_system":"ioi","source_type":"tasks","zero_penalty":5,"summary_total_only":true,"subcontests":[]}`)
	if code != http.StatusOK {
		t.Fatalf("contest save with zero_penalty failed: code=%d", code)
	}
	contestsBody, _ := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if !strings.Contains(string(contestsBody), `"zero_penalty": 5`) && !strings.Contains(string(contestsBody), `"zero_penalty":5`) {
		t.Fatalf("zero_penalty not saved: %s", contestsBody)
	}
	if !strings.Contains(string(contestsBody), `"summary_total_only": true`) && !strings.Contains(string(contestsBody), `"summary_total_only":true`) {
		t.Fatalf("summary_total_only not saved: %s", contestsBody)
	}
	if !strings.Contains(string(contestsBody), `"short_name": "Ол-1"`) && !strings.Contains(string(contestsBody), `"short_name":"Ол-1"`) {
		t.Fatalf("short_name not saved: %s", contestsBody)
	}
	code, _ = postJSON(t, h.AdminContestSave,
		`{"id":"c9","score_system":"ioi","source_type":"tasks","zero_penalty":-3,"subcontests":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("negative zero_penalty must be rejected, got code=%d", code)
	}

	// Глобальная заморозка в определении контеста: сохраняется; невалидная — отказ.
	code, _ = postJSON(t, h.AdminContestSave,
		`{"original_id":"c1","id":"c1","title":"К1","score_system":"ioi","source_type":"tasks","freeze":"1h","subcontests":[]}`)
	if code != http.StatusOK {
		t.Fatalf("contest save with freeze failed: code=%d", code)
	}
	contestsBody, _ = os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if !strings.Contains(string(contestsBody), `"freeze": "1h"`) && !strings.Contains(string(contestsBody), `"freeze":"1h"`) {
		t.Fatalf("contest freeze not saved: %s", contestsBody)
	}
	code, _ = postJSON(t, h.AdminContestSave,
		`{"id":"c8","score_system":"ioi","source_type":"tasks","freeze":"скоро","subcontests":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid contest freeze must be rejected, got code=%d", code)
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

// Токенный просмотр: с верным токеном RowsFull/GradesFull подменяют публичные,
// без токена — вырезаются из ответа. Токен управляется админ-эндпоинтом и
// переживает перезапись group.json (grades не теряются).
func TestGroupSecretTokenFlow(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	writeTestFile(t, filepath.Join(dataDir, "groups", "grp", "group.json"),
		`{"title":"Т","student_ids":["s1"],"grades":{"columns":[{"id":"z","title":"З","weight":1,"type":"manual"}]}}`)

	// Генерация токена.
	code, resp := postJSON(t, h.AdminGroupTokenSet, `{"slug":"grp","clear":false}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("token generate failed: code=%d resp=%v", code, resp)
	}
	token, _ := resp["token"].(string)
	if len(token) != 32 {
		t.Fatalf("unexpected token: %q", token)
	}
	var gf domain.GroupFile
	body, _ := os.ReadFile(filepath.Join(dataDir, "groups", "grp", "group.json"))
	if err := json.Unmarshal(body, &gf); err != nil || gf.GroupSecretToken != token {
		t.Fatalf("token not saved: %v %s", err, body)
	}
	if gf.Grades == nil || len(gf.Grades.Columns) != 1 {
		t.Fatalf("grades lost on token write: %s", body)
	}

	// applyFreezeView: без токена — full-варианты вырезаются.
	frozenAt := time.Now().Add(-time.Hour)
	makeStandings := func() domain.GeneratedGroupStandings {
		return domain.GeneratedGroupStandings{
			GroupSlug:  "grp",
			Grades:     &domain.GeneratedGrades{Title: "Замороженные"},
			GradesFull: &domain.GeneratedGrades{Title: "Полные"},
			Contests: []domain.GeneratedContestStandings{{
				ID: "c", FrozenAt: &frozenAt,
				Rows:     []domain.GeneratedRow{{StudentID: "s1", SolvedCount: 1}},
				RowsFull: []domain.GeneratedRow{{StudentID: "s1", SolvedCount: 5}},
			}},
		}
	}

	s := makeStandings()
	req := httptest.NewRequest(http.MethodGet, "/standings/grp", nil)
	if h.applyFreezeView(&s, "grp", req) {
		t.Fatal("no token must not unfreeze")
	}
	if s.Contests[0].RowsFull != nil || s.GradesFull != nil || s.Contests[0].Rows[0].SolvedCount != 1 {
		t.Fatalf("full variants must be stripped: %+v", s)
	}

	// Неверный токен — тоже публичная версия.
	s = makeStandings()
	req = httptest.NewRequest(http.MethodGet, "/standings/grp?token=wrong", nil)
	if h.applyFreezeView(&s, "grp", req) {
		t.Fatal("wrong token must not unfreeze")
	}

	// Верный токен — полная версия, full-поля убраны из ответа.
	s = makeStandings()
	req = httptest.NewRequest(http.MethodGet, "/standings/grp?token="+token, nil)
	if !h.applyFreezeView(&s, "grp", req) {
		t.Fatal("valid token must unfreeze")
	}
	if s.Contests[0].Rows[0].SolvedCount != 5 || s.Grades.Title != "Полные" {
		t.Fatalf("full variants must be swapped in: %+v", s)
	}
	if s.Contests[0].RowsFull != nil || s.GradesFull != nil {
		t.Fatalf("swapped response must not carry full duplicates: %+v", s)
	}

	// Удаление токена — доступ закрыт сразу.
	code, resp = postJSON(t, h.AdminGroupTokenSet, `{"slug":"grp","clear":true}`)
	if code != http.StatusOK || resp["token"] != "" {
		t.Fatalf("token clear failed: code=%d resp=%v", code, resp)
	}
	s = makeStandings()
	req = httptest.NewRequest(http.MethodGet, "/standings/grp?token="+token, nil)
	if h.applyFreezeView(&s, "grp", req) {
		t.Fatal("cleared token must not unfreeze")
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

// SerializeDataWrite защищает от потери записи при конкурентных админ-операциях:
// N параллельных добавлений разных учеников — все N должны оказаться в файле
// (без мьютекса read-modify-write students.json терял бы обновления).
func TestSerializeDataWriteNoLostUpdate(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "students.json"), `[]`)

	handler := h.SerializeDataWrite(h.AdminStudentSave)
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"full_name":"Ученик Номер %d","accounts":[]}`, i)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("save %d: code=%d", i, rec.Code)
			}
		}(i)
	}
	wg.Wait()

	body, err := os.ReadFile(filepath.Join(dataDir, "students.json"))
	if err != nil {
		t.Fatal(err)
	}
	var students []map[string]any
	if err := json.Unmarshal(body, &students); err != nil {
		t.Fatalf("students.json corrupted: %v", err)
	}
	if len(students) != n {
		t.Fatalf("lost updates: got %d students, want %d", len(students), n)
	}
}

// Конструктор столбцов оценок: строит и валидирует grades в group.json,
// не трогая остальные поля группы; пустой список — убирает оценки.
func TestAdminGroupGradesConfigSave(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "groups", "grp", "group.json"),
		`{"title":"Группа","form_link":"https://f","student_ids":["s1","s2"],"group_secret_token":"tok123"}`)

	// Сохранение конфига: table-столбец + manual-столбец.
	code, resp := postJSON(t, h.AdminGroupGradesConfigSave, `{
		"slug":"grp","title":"Оценки","round":2,
		"columns":[
			{"id":"educational","title":"Тематические","weight":0.35,"type":"table","table_name":"Тематические","metric":"plus","normalize":"max","upsolving":0.5},
			{"id":"olymp","title":"Соревнования","weight":0.35,"type":"table","metric":"score","normalize":25},
			{"id":"zachet","title":"Зачет","weight":0.3,"type":"manual"}
		]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("config save failed: code=%d resp=%v", code, resp)
	}

	var gf domain.GroupFile
	body, _ := os.ReadFile(filepath.Join(dataDir, "groups", "grp", "group.json"))
	if err := json.Unmarshal(body, &gf); err != nil {
		t.Fatalf("group.json broken: %v; %s", err, body)
	}
	// Прочие поля группы сохранены.
	if gf.Title != "Группа" || gf.FormLink != "https://f" || gf.GroupSecretToken != "tok123" || len(gf.StudentIDs) != 2 {
		t.Fatalf("group fields lost: %s", body)
	}
	if gf.Grades == nil || len(gf.Grades.Columns) != 3 || gf.Grades.Title != "Оценки" || gf.Grades.Round == nil || *gf.Grades.Round != 2 {
		t.Fatalf("grades not saved: %s", body)
	}
	c0 := gf.Grades.Columns[0]
	if c0.ID != "educational" || c0.Type != "table" || c0.TableName != "Тематические" || c0.Metric != "plus" ||
		c0.Normalize.Mode != domain.NormalizeMax || c0.Upsolving == nil || *c0.Upsolving != 0.5 {
		t.Fatalf("table column wrong: %+v", c0)
	}
	if gf.Grades.Columns[1].Normalize.Mode != domain.NormalizeFixed || gf.Grades.Columns[1].Normalize.Value != 25 {
		t.Fatalf("fixed normalize wrong: %+v", gf.Grades.Columns[1])
	}
	if gf.Grades.Columns[2].Type != "manual" {
		t.Fatalf("manual column wrong: %+v", gf.Grades.Columns[2])
	}
	// Файл читается повторно (round-trip normalize не ломается).
	if _, ok, err := h.readGroupFile("grp"); err != nil || !ok {
		t.Fatalf("re-read group.json failed: %v", err)
	}

	// Пустой список столбцов — секция оценок убирается.
	code, _ = postJSON(t, h.AdminGroupGradesConfigSave, `{"slug":"grp","columns":[]}`)
	if code != http.StatusOK {
		t.Fatalf("empty columns save failed: code=%d", code)
	}
	body, _ = os.ReadFile(filepath.Join(dataDir, "groups", "grp", "group.json"))
	var gf2 domain.GroupFile
	if err := json.Unmarshal(body, &gf2); err != nil {
		t.Fatal(err)
	}
	if gf2.Grades != nil {
		t.Fatalf("empty columns must drop grades: %s", body)
	}
	if gf2.GroupSecretToken != "tok123" || len(gf2.StudentIDs) != 2 {
		t.Fatalf("empty columns must keep other group fields: %s", body)
	}

	// Валидация: пустое название, дубль id, чужой тип, upsolving вне [0,1].
	bad := []string{
		`{"slug":"grp","columns":[{"title":"","type":"manual"}]}`,
		`{"slug":"grp","columns":[{"id":"x","title":"A","type":"manual"},{"id":"x","title":"B","type":"manual"}]}`,
		`{"slug":"grp","columns":[{"title":"A","type":"weird"}]}`,
		`{"slug":"grp","columns":[{"title":"A","type":"table","upsolving":2}]}`,
		`{"slug":"grp","columns":[{"title":"A","type":"table","weight":-1}]}`,
	}
	for i, b := range bad {
		code, _ = postJSON(t, h.AdminGroupGradesConfigSave, b)
		if code != http.StatusBadRequest {
			t.Fatalf("bad case #%d must be rejected, got code=%d", i, code)
		}
	}
}
