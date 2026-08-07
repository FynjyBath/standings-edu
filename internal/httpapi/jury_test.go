package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
)

// juryTestSetup: группа с токеном, глобальный контест, inline-кондуит и ручной
// столбец оценок.
func juryTestSetup(t *testing.T) (*Handlers, string) {
	t.Helper()
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)

	mustWrite := func(path string, v string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(dataDir, "students.json"),
		`[{"id":"s1","full_name":"Иванов Иван","public_name":"Иванов И."}]`)
	mustWrite(filepath.Join(dataDir, "contests.json"),
		`[{"id":"global1","title":"Глобальный","score_system":"edu","subcontests":[]},
		  {"id":"gkond","title":"Общий кондуит","score_system":"edu","source_type":"provider",
		   "provider":"manual_table","provider_config":{"table":"","task_count":2},"subcontests":[]}]`)
	mustWrite(filepath.Join(dataDir, "groups", "g1", "group.json"),
		`{"title":"Группа","group_secret_token":"tok","student_ids":["s1"],
		  "panel_access":{"jury":{"login":"j","password":"jp"},"admin":{"login":"a","password":"ap"}},
		  "grades":{"columns":[{"id":"activity","title":"Активность","weight":1,"type":"manual"}]}}`)
	mustWrite(filepath.Join(dataDir, "groups", "g1", "contests.json"),
		`[{"id":"kond","title":"Кондуит","score_system":"edu","source_type":"provider",
		   "provider":"manual_table","provider_config":{"table":"ФИО\t1\nИванов Иван\t1\n","task_count":1},
		   "subcontests":[],"update":true},
		  {"id":"other","update":true}]`)
	return h, dataDir
}

// juryRoleToken/adminRoleToken — подписи ролей панели тестовой группы.
func juryRoleToken(h *Handlers) string {
	return h.roleTokenFor("g1", RoleJury, &domain.GroupPanelCredential{Login: "j", Password: "jp"})
}

func adminRoleToken(h *Handlers) string {
	return h.roleTokenFor("g1", RoleAdmin, &domain.GroupPanelCredential{Login: "a", Password: "ap"})
}

// panelGet — запрос к странице панели с Basic Auth роли.
func panelGet(t *testing.T, handler http.HandlerFunc, target, slug, login, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("group_name", slug)
	if login != "" {
		req.SetBasicAuth(login, password)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func juryPost(t *testing.T, handler http.HandlerFunc, body map[string]any) (int, map[string]any) {
	t.Helper()
	blob, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(blob))
	rec := httptest.NewRecorder()
	handler(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// Неверный токен — 403 на всех жюри-ручках, файлы не меняются.
func TestJuryRejectsBadToken(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	before, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))

	cases := map[string]http.HandlerFunc{
		"add":     h.PanelContestAddRef,
		"move":    h.PanelContestMove,
		"grades":  h.PanelGradesSave,
		"konduit": h.JuryKonduitSave,
	}
	for name, fn := range cases {
		code, _ := juryPost(t, fn, map[string]any{
			"slug": "g1", "role_token": "WRONG", "id": "kond", "dir": "up",
			"table": "x\t1\n", "task_count": 1,
			"grades": map[string]map[string]float64{"activity": {"s1": 5}},
		})
		if code != http.StatusForbidden {
			t.Errorf("%s: code=%d want 403", name, code)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("files must be untouched on bad token")
	}
	// Токена группы для операций теперь НЕДОСТАТОЧНО — только просмотр.
	for name, fn := range cases {
		code, _ := juryPost(t, fn, map[string]any{
			"slug": "g1", "role_token": "tok", "id": "kond", "dir": "up",
			"table": "x\t1\n", "task_count": 1,
			"grades": map[string]map[string]float64{"activity": {"s1": 5}},
		})
		if code != http.StatusForbidden {
			t.Errorf("%s: токен группы не должен давать прав, code=%d", name, code)
		}
	}
	// Страницы панели без логина — 401 (браузер спросит пароль).
	if rec := panelGet(t, h.GroupPanelGradesPage, "/standings/g1/panel/grades", "g1", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("panel grades без логина: code=%d want 401", rec.Code)
	}
	if rec := panelGet(t, h.GroupPanelGradesPage, "/standings/g1/panel/grades", "g1", "j", "WRONG"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("panel grades с чужим паролем: code=%d want 401", rec.Code)
	}
}

// Верный токен: добавление глобального контеста СВЕРХУ и перестановка работают.
func TestJuryContestAddAndMove(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	code, resp := juryPost(t, h.PanelContestAddRef, map[string]any{"slug": "g1", "role_token": adminRoleToken(h), "id": "global1"})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("add-ref: %d %v", code, resp)
	}
	var entries []map[string]any
	blob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	_ = json.Unmarshal(blob, &entries)
	if len(entries) != 3 || entries[0]["id"] != "global1" {
		t.Fatalf("global1 must be first: %v", entries)
	}

	// Дубликат — ошибка.
	if code, _ := juryPost(t, h.PanelContestAddRef, map[string]any{"slug": "g1", "role_token": adminRoleToken(h), "id": "global1"}); code == http.StatusOK {
		t.Fatal("duplicate add must fail")
	}
	// Несуществующий глобальный — ошибка.
	if code, _ := juryPost(t, h.PanelContestAddRef, map[string]any{"slug": "g1", "role_token": adminRoleToken(h), "id": "nope"}); code == http.StatusOK {
		t.Fatal("unknown global must fail")
	}

	code, resp = juryPost(t, h.PanelContestMove, map[string]any{"slug": "g1", "role_token": adminRoleToken(h), "id": "global1", "dir": "down"})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("move: %d %v", code, resp)
	}
	blob, _ = os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	_ = json.Unmarshal(blob, &entries)
	if entries[0]["id"] != "kond" || entries[1]["id"] != "global1" {
		t.Fatalf("move failed: %v", entries)
	}
}

// Кондуит: сохранение пишет таблицу в manual_tables.json группы, а в
// определении inline-контеста остаётся только конфиг (без оценок);
// ссылки и чужие типы не редактируются.
func TestJuryKonduitSave(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	code, resp := juryPost(t, h.JuryKonduitSave, map[string]any{
		"slug": "g1", "role_token": juryRoleToken(h), "id": "kond",
		"table": "ФИО\t1\t2\nИванов Иван\t1\t+\n", "task_count": 2,
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("konduit save: %d %v", code, resp)
	}
	var entries []map[string]any
	blob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	_ = json.Unmarshal(blob, &entries)
	cfg := entries[0]["provider_config"].(map[string]any)
	if cfg["task_count"].(float64) != 2 {
		t.Fatalf("config not updated: %v", cfg)
	}
	if _, hasTable := cfg["table"]; hasTable {
		t.Fatalf("table must move out of provider_config: %v", cfg)
	}
	if entries[0]["title"] != "Кондуит" || entries[0]["provider"] != "manual_table" {
		t.Fatalf("other fields must survive: %v", entries[0])
	}
	// Таблица — в отдельном файле группы.
	var tables map[string]string
	tblob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "manual_tables.json"))
	_ = json.Unmarshal(tblob, &tables)
	if !strings.Contains(tables["kond"], "Иванов Иван") {
		t.Fatalf("table must be in group manual_tables.json: %v", tables)
	}
	// Редактор видит сохранённую таблицу (из нового файла).
	rec := panelGet(t, h.JuryKonduitPage, "/standings/g1/jury-konduit?id=kond", "g1", "j", "jp")
	if !bytes.Contains(rec.Body.Bytes(), []byte(`value="&#43;"`)) {
		t.Fatal("editor must show saved grades from manual_tables.json")
	}

	// Ссылка на контест без глобального manual_table-определения — отказ.
	if code, _ := juryPost(t, h.JuryKonduitSave, map[string]any{
		"slug": "g1", "role_token": juryRoleToken(h), "id": "other", "table": "x\t1\n", "task_count": 1,
	}); code == http.StatusOK {
		t.Fatal("ref to non-manual contest must be rejected")
	}
}

// Общий кондуит по ссылке: жюри одной группы пишет в глобальный contests.json,
// вторая группа с тем же контестом видит те же оценки (и дописывает свои).
func TestJuryKonduitSharedAcrossGroups(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	mustWrite := func(path, v string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Обе группы ссылаются на глобальный кондуит gkond.
	mustWrite(filepath.Join(dataDir, "students.json"),
		`[{"id":"s1","full_name":"Иванов Иван","public_name":"Иванов И."},
		  {"id":"s2","full_name":"Петров Пётр","public_name":"Петров П."}]`)
	mustWrite(filepath.Join(dataDir, "groups", "g1", "contests.json"), `[{"id":"gkond","update":true}]`)
	mustWrite(filepath.Join(dataDir, "groups", "g2", "group.json"),
		`{"title":"Группа 2","group_secret_token":"tok2","student_ids":["s2"],"panel_access":{"jury":{"login":"j2","password":"jp2"}}}`)
	mustWrite(filepath.Join(dataDir, "groups", "g2", "contests.json"), `[{"id":"gkond","update":true}]`)

	// Жюри группы 1 заполняет своего ученика.
	code, resp := juryPost(t, h.JuryKonduitSave, map[string]any{
		"slug": "g1", "role_token": juryRoleToken(h), "id": "gkond",
		"table": "ФИО\t1\t2\nИванов Иван\t1\t\n", "task_count": 2,
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("g1 save: %d %v", code, resp)
	}
	// Оценка ушла в ГЛОБАЛЬНЫЙ manual_tables.json (не в определение контеста).
	blob, _ := os.ReadFile(filepath.Join(dataDir, "manual_tables.json"))
	if !bytes.Contains(blob, []byte("Иванов Иван")) {
		t.Fatal("grade must be stored in global manual_tables.json")
	}
	cblob, _ := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if bytes.Contains(cblob, []byte("Иванов Иван")) {
		t.Fatal("contest definition must stay free of grades")
	}

	// Редактор группы 2: только СВОЙ ученик (Петров); чужой Иванов скрыт.
	rec := panelGet(t, h.JuryKonduitPage, "/standings/g2/jury-konduit?id=gkond", "g2", "j2", "jp2")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !bytes.Contains([]byte(body), []byte("Петров Пётр")) {
		t.Fatalf("g2 editor must show own student: code=%d", rec.Code)
	}
	if bytes.Contains([]byte(body), []byte("Иванов Иван")) {
		t.Fatal("g2 editor must NOT show another group's rows")
	}
	if !bytes.Contains([]byte(body), []byte("Общий кондуит")) {
		t.Fatal("shared konduit title expected")
	}

	// Жюри группы 2 сохраняет ТОЛЬКО своего Петрова — строка Иванова из
	// группы 1 в общем конфиге не теряется (merge).
	code, resp = juryPost(t, h.JuryKonduitSave, map[string]any{
		"slug": "g2", "role_token": h.roleTokenFor("g2", RoleJury, &domain.GroupPanelCredential{Login: "j2", Password: "jp2"}), "id": "gkond",
		"table": "ФИО\t1\t2\nПетров Пётр\t\t+\n", "task_count": 2,
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("g2 save: %d %v", code, resp)
	}
	blob, _ = os.ReadFile(filepath.Join(dataDir, "manual_tables.json"))
	if !bytes.Contains(blob, []byte("Иванов Иван")) || !bytes.Contains(blob, []byte("Петров Пётр")) {
		t.Fatalf("both groups' rows must be in global manual_tables.json: %s", blob)
	}

	// Ученик в двух группах: если Иванова добавить и в группу 2, его строка
	// с уже проставленными оценками видна в редакторе группы 2.
	mustWrite(filepath.Join(dataDir, "groups", "g2", "group.json"),
		`{"title":"Группа 2","group_secret_token":"tok2","student_ids":["s1","s2"],"panel_access":{"jury":{"login":"j2","password":"jp2"}}}`)
	rec = panelGet(t, h.JuryKonduitPage, "/standings/g2/jury-konduit?id=gkond", "g2", "j2", "jp2")
	if !bytes.Contains(rec.Body.Bytes(), []byte("Иванов Иван")) {
		t.Fatal("shared student's existing row must appear in g2 editor")
	}

	// Группные contests.json не тронуты (там только ссылки).
	gblob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	if bytes.Contains(gblob, []byte("Петров")) || bytes.Contains(gblob, []byte("provider_config")) {
		t.Fatalf("group file must stay a plain ref: %s", gblob)
	}
}

// Создание кондуита жюри: только название и число задач; inline manual_table
// с автоматическим id добавляется в начало списка; редактор сразу работает.
func TestJuryKonduitCreate(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	// Валидация.
	if code, _ := juryPost(t, h.JuryKonduitCreate, map[string]any{"slug": "g1", "role_token": juryRoleToken(h), "title": "  ", "task_count": 5}); code == http.StatusOK {
		t.Fatal("empty title must fail")
	}
	if code, _ := juryPost(t, h.JuryKonduitCreate, map[string]any{"slug": "g1", "role_token": juryRoleToken(h), "title": "X", "task_count": 0}); code == http.StatusOK {
		t.Fatal("zero tasks must fail")
	}
	if code, _ := juryPost(t, h.JuryKonduitCreate, map[string]any{"slug": "g1", "token": "BAD", "title": "X", "task_count": 5}); code != http.StatusForbidden {
		t.Fatal("bad token must be rejected")
	}

	code, resp := juryPost(t, h.JuryKonduitCreate, map[string]any{"slug": "g1", "role_token": juryRoleToken(h), "title": "Дз №3", "task_count": 5})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("create: %d %v", code, resp)
	}
	id, _ := resp["id"].(string)
	if !strings.HasPrefix(id, "konduit-") {
		t.Fatalf("auto id expected, got %q", id)
	}

	// Запись — inline manual_table в начале списка, только нужные поля.
	var entries []map[string]any
	blob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	_ = json.Unmarshal(blob, &entries)
	first := entries[0]
	if first["id"] != id || first["provider"] != "manual_table" || first["title"] != "Дз №3" || first["score_system"] != "edu" {
		t.Fatalf("created entry wrong: %v", first)
	}
	cfg := first["provider_config"].(map[string]any)
	if cfg["task_count"].(float64) != 5 {
		t.Fatalf("task_count wrong: %v", cfg)
	}
	if _, hasTable := cfg["table"]; hasTable {
		t.Fatalf("new konduit must have no table in config: %v", cfg)
	}

	// Редактор открывается сразу: 5 колонок, ученики группы подставлены.
	rec := panelGet(t, h.JuryKonduitPage, "/standings/g1/jury-konduit?id="+id, "g1", "j", "jp")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Иванов Иван") || !strings.Contains(body, "Дз №3") {
		t.Fatalf("editor must open for created konduit: code=%d", rec.Code)
	}
}

// Ручные оценки по токену: пишутся только известные столбцы и ученики группы.
func TestJuryGradesSave(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	code, resp := juryPost(t, h.PanelGradesSave, map[string]any{
		"slug": "g1", "role_token": juryRoleToken(h),
		"grades": map[string]map[string]float64{
			"activity": {"s1": 8.5, "stranger": 3},
			"unknown":  {"s1": 1},
		},
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("grades save: %d %v", code, resp)
	}
	blob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "grades_manual.json"))
	var saved map[string]map[string]float64
	_ = json.Unmarshal(blob, &saved)
	if saved["activity"]["s1"] != 8.5 {
		t.Fatalf("grade not saved: %v", saved)
	}
	if _, ok := saved["unknown"]; ok {
		t.Fatal("unknown column must be dropped")
	}
	if _, ok := saved["activity"]["stranger"]; ok {
		t.Fatal("stranger student must be dropped")
	}
}

// Страница кондуита: колонки из конфига, существующие строки + недостающие
// ученики группы.
func TestJuryKonduitPage(t *testing.T) {
	h, _ := juryTestSetup(t)
	rec := panelGet(t, h.JuryKonduitPage, "/standings/g1/jury-konduit?id=kond", "g1", "j", "jp")
	if rec.Code != http.StatusOK {
		t.Fatalf("page code=%d body=%s", rec.Code, rec.Body.String()[:200])
	}
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("Иванов Иван")) {
		t.Fatal("existing konduit row must be rendered")
	}
	if !bytes.Contains([]byte(body), []byte("k-save")) {
		t.Fatal("save button must be rendered")
	}
}

// Админ группы добавляет inline-контест из панели: та же форма, что в админке,
// но окно (начало/конец) обязательно у контестов-наборов задач.
func TestPanelContestInlineSave(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	save := func(body map[string]any) (int, map[string]any) { return juryPost(t, h.PanelContestInlineSave, body) }

	base := map[string]any{
		"slug": "g1", "role_token": adminRoleToken(h), "id": "olymp1", "title": "Олимпиада",
		"score_system": "ioi", "table_name": "Соревнования",
		"subcontests": []map[string]any{{"title": "Задачи", "tasks": []string{"https://codeforces.com/gym/105519"}}},
		"start_time":  "2026-09-01T09:00:00Z", "end_time": "2026-09-01T12:00:00Z",
		"freeze": "1h",
	}
	code, resp := save(base)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("valid inline save: %d %v", code, resp)
	}
	// Контест записан в contests.json группы, сверху, с окном.
	var entries []map[string]any
	blob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	_ = json.Unmarshal(blob, &entries)
	if entries[0]["id"] != "olymp1" || entries[0]["start_time"] == nil || entries[0]["end_time"] == nil {
		t.Fatalf("inline контест должен быть сверху с окном: %+v", entries[0])
	}

	// Без окна — 400 (в админке это разрешено, в панели нет).
	noTime := map[string]any{"slug": "g1", "role_token": adminRoleToken(h), "id": "x",
		"subcontests": []map[string]any{{"title": "З", "tasks": []string{"https://acmp.ru/?main=task&id_task=1"}}}}
	if code, _ := save(noTime); code != http.StatusBadRequest {
		t.Errorf("без окна ожидался 400, got %d", code)
	}
	// provider-контест окно не использует — сохраняется без него.
	prov := map[string]any{"slug": "g1", "role_token": adminRoleToken(h), "id": "kond2",
		"title": "Кондуит 2", "source_type": "provider", "provider": "manual_table",
		"provider_config": `{"task_count":3}`}
	if code, resp := save(prov); code != http.StatusOK {
		t.Errorf("provider без окна должен сохраняться: %d %v", code, resp)
	}
	// Роль жюри управлять контестами не может.
	juryTry := map[string]any{"slug": "g1", "role_token": juryRoleToken(h), "id": "y",
		"subcontests": []map[string]any{{"title": "З", "tasks": []string{"https://acmp.ru/?main=task&id_task=2"}}},
		"start_time":  "2026-09-01T09:00:00Z", "end_time": "2026-09-01T12:00:00Z"}
	if code, _ := save(juryTry); code != http.StatusForbidden {
		t.Errorf("жюри не должно управлять контестами, got %d", code)
	}
}
