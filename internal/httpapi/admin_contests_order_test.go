package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contestIDs — порядок id в data/contests.json.
func contestIDs(t *testing.T, dataDir string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("contests.json не разбирается: %v", err)
	}
	out := make([]string, 0, len(list))
	for _, c := range list {
		id, _ := c["id"].(string)
		out = append(out, id)
	}
	return out
}

func setupContestsOrder(t *testing.T) (*Handlers, string) {
	t.Helper()
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	writeTestFile(t, filepath.Join(dataDir, "contests.json"), `[
	 {"id":"week_10","title":"Неделя 10","score_system":"edu","subcontests":[]},
	 {"id":"week_9","title":"Неделя 9","score_system":"edu","subcontests":[],"custom_field":"хвост"},
	 {"id":"alpha","title":"Альфа","score_system":"ioi","subcontests":[]},
	 {"id":"week_2","title":"Неделя 2","score_system":"edu","subcontests":[]}]`)
	return h, dataDir
}

// Перестановка соседей: меняется только порядок, содержимое записей — нет.
func TestAdminContestMove(t *testing.T) {
	h, dataDir := setupContestsOrder(t)

	if code, resp := postJSON(t, h.AdminContestMove, `{"id":"alpha","dir":"up"}`); code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("move up: code=%d resp=%v", code, resp)
	}
	if got := contestIDs(t, dataDir); strings.Join(got, ",") != "week_10,alpha,week_9,week_2" {
		t.Fatalf("после подъёма: %v", got)
	}
	if code, _ := postJSON(t, h.AdminContestMove, `{"id":"week_2","dir":"down"}`); code != http.StatusBadRequest {
		t.Errorf("нижний контест двигать некуда: code=%d, ожидался 400", code)
	}
	if code, _ := postJSON(t, h.AdminContestMove, `{"id":"week_10","dir":"up"}`); code != http.StatusBadRequest {
		t.Errorf("верхний контест двигать некуда: code=%d, ожидался 400", code)
	}
	if code, _ := postJSON(t, h.AdminContestMove, `{"id":"нет-такого","dir":"up"}`); code != http.StatusBadRequest {
		t.Errorf("несуществующий id: code=%d, ожидался 400", code)
	}
	if code, _ := postJSON(t, h.AdminContestMove, `{"id":"alpha","dir":"вбок"}`); code != http.StatusBadRequest {
		t.Errorf("кривое направление: code=%d, ожидался 400", code)
	}

	// Записи не переписываются: неизвестные поля остаются на месте.
	body, err := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"custom_field": "хвост"`) {
		t.Fatalf("перестановка не должна терять поля записи: %s", body)
	}
}

// Сортировка по id — «по-человечески»: week_9 перед week_10.
func TestAdminContestsSort(t *testing.T) {
	h, dataDir := setupContestsOrder(t)

	code, resp := postJSON(t, h.AdminContestsSort, `{}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("sort: code=%d resp=%v", code, resp)
	}
	if got := contestIDs(t, dataDir); strings.Join(got, ",") != "alpha,week_2,week_9,week_10" {
		t.Fatalf("порядок после сортировки: %v", got)
	}
	// Повторная сортировка ничего не меняет и на пустом списке не падает.
	if code, _ := postJSON(t, h.AdminContestsSort, `{}`); code != http.StatusOK {
		t.Errorf("повторная сортировка: code=%d", code)
	}
	if got := contestIDs(t, dataDir); strings.Join(got, ",") != "alpha,week_2,week_9,week_10" {
		t.Fatalf("сортировка должна быть устойчивой: %v", got)
	}
}

func TestNaturalLess(t *testing.T) {
	ordered := []string{"alpha", "beta2", "beta10", "week_2", "week_09", "week_9", "week_10", "week_10a"}
	for i := 0; i+1 < len(ordered); i++ {
		a, b := ordered[i], ordered[i+1]
		if !naturalLess(a, b) {
			t.Errorf("%q должно идти перед %q", a, b)
		}
		if naturalLess(b, a) {
			t.Errorf("%q не должно идти перед %q", b, a)
		}
	}
	if naturalLess("same", "same") {
		t.Error("равные строки не меньше друг друга")
	}
}
