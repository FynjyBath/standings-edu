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
		  "grades":{"columns":[{"id":"activity","title":"Активность","weight":1,"type":"manual"}]}}`)
	mustWrite(filepath.Join(dataDir, "groups", "g1", "contests.json"),
		`[{"id":"kond","title":"Кондуит","score_system":"edu","source_type":"provider",
		   "provider":"manual_table","provider_config":{"table":"ФИО\t1\nИванов Иван\t1\n","task_count":1},
		   "subcontests":[],"update":true},
		  {"id":"other","update":true}]`)
	return h, dataDir
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
		"add":     h.JuryContestAddRef,
		"move":    h.JuryContestMove,
		"grades":  h.JuryGradesSave,
		"konduit": h.JuryKonduitSave,
	}
	for name, fn := range cases {
		code, _ := juryPost(t, fn, map[string]any{
			"slug": "g1", "token": "WRONG", "id": "kond", "dir": "up",
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
	// Страницы тоже закрыты.
	req := httptest.NewRequest(http.MethodGet, "/standings/g1/jury-grades?token=WRONG", nil)
	req.SetPathValue("group_name", "g1")
	rec := httptest.NewRecorder()
	h.JuryGradesPage(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("jury grades page must 404 on bad token, got %d", rec.Code)
	}
}

// Верный токен: добавление глобального контеста СВЕРХУ и перестановка работают.
func TestJuryContestAddAndMove(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	code, resp := juryPost(t, h.JuryContestAddRef, map[string]any{"slug": "g1", "token": "tok", "id": "global1"})
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
	if code, _ := juryPost(t, h.JuryContestAddRef, map[string]any{"slug": "g1", "token": "tok", "id": "global1"}); code == http.StatusOK {
		t.Fatal("duplicate add must fail")
	}
	// Несуществующий глобальный — ошибка.
	if code, _ := juryPost(t, h.JuryContestAddRef, map[string]any{"slug": "g1", "token": "tok", "id": "nope"}); code == http.StatusOK {
		t.Fatal("unknown global must fail")
	}

	code, resp = juryPost(t, h.JuryContestMove, map[string]any{"slug": "g1", "token": "tok", "id": "global1", "dir": "down"})
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
		"slug": "g1", "token": "tok", "id": "kond",
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
	req := httptest.NewRequest(http.MethodGet, "/standings/g1/jury-konduit?id=kond&token=tok", nil)
	req.SetPathValue("group_name", "g1")
	rec := httptest.NewRecorder()
	h.JuryKonduitPage(rec, req)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`value="&#43;"`)) {
		t.Fatal("editor must show saved grades from manual_tables.json")
	}

	// Ссылка на контест без глобального manual_table-определения — отказ.
	if code, _ := juryPost(t, h.JuryKonduitSave, map[string]any{
		"slug": "g1", "token": "tok", "id": "other", "table": "x\t1\n", "task_count": 1,
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
		`{"title":"Группа 2","group_secret_token":"tok2","student_ids":["s2"]}`)
	mustWrite(filepath.Join(dataDir, "groups", "g2", "contests.json"), `[{"id":"gkond","update":true}]`)

	// Жюри группы 1 заполняет своего ученика.
	code, resp := juryPost(t, h.JuryKonduitSave, map[string]any{
		"slug": "g1", "token": "tok", "id": "gkond",
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
	req := httptest.NewRequest(http.MethodGet, "/standings/g2/jury-konduit?id=gkond&token=tok2", nil)
	req.SetPathValue("group_name", "g2")
	rec := httptest.NewRecorder()
	h.JuryKonduitPage(rec, req)
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
		"slug": "g2", "token": "tok2", "id": "gkond",
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
		`{"title":"Группа 2","group_secret_token":"tok2","student_ids":["s1","s2"]}`)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/standings/g2/jury-konduit?id=gkond&token=tok2", nil)
	req.SetPathValue("group_name", "g2")
	h.JuryKonduitPage(rec, req)
	if !bytes.Contains(rec.Body.Bytes(), []byte("Иванов Иван")) {
		t.Fatal("shared student's existing row must appear in g2 editor")
	}

	// Группные contests.json не тронуты (там только ссылки).
	gblob, _ := os.ReadFile(filepath.Join(dataDir, "groups", "g1", "contests.json"))
	if bytes.Contains(gblob, []byte("Петров")) || bytes.Contains(gblob, []byte("provider_config")) {
		t.Fatalf("group file must stay a plain ref: %s", gblob)
	}
}

// Ручные оценки по токену: пишутся только известные столбцы и ученики группы.
func TestJuryGradesSave(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	code, resp := juryPost(t, h.JuryGradesSave, map[string]any{
		"slug": "g1", "token": "tok",
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
	req := httptest.NewRequest(http.MethodGet, "/standings/g1/jury-konduit?id=kond&token=tok", nil)
	req.SetPathValue("group_name", "g1")
	rec := httptest.NewRecorder()
	h.JuryKonduitPage(rec, req)
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
