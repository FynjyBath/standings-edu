package httpapi

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

// Задачи ejudge: ученикам — клиентские ссылки (new-client), преподавателям (по
// токену и в панели) — судейские (new-judge?contest_id=…).
func TestEjudgeLinksJudgeModeForStaff(t *testing.T) {
	dataDir := t.TempDir()
	genDir := t.TempDir()
	h := NewHandlers(
		storage.NewGeneratedLoader(genDir),
		nil,
		web.NewTemplateRenderer(filepath.Join("..", "..", "web", "templates")),
		log.New(io.Discard, "", 0),
	)
	if err := h.ConfigureAdmin(AdminConfig{
		Login: "admin", Password: "pw", ProjectRoot: t.TempDir(), DataDir: dataDir,
	}); err != nil {
		t.Fatal(err)
	}
	h.ConfigureSourceDir(dataDir)

	writeTestFile(t, filepath.Join(dataDir, "groups", "g1", "group.json"),
		`{"title":"Г1","student_ids":["s1"],"group_secret_token":"tok",
		  "panel_access":{"jury":{"login":"j","password":"jp"}}}`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
		"group_slug":"g1","group_title":"Г1","contests":[{
			"id":"c1","title":"Контест","score_system":"edu",
			"materials":[{"title":"Условия","url":"https://ej.kod-u.ru/new-client?contest_id=777"}],
			"tasks":[
				{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3","normalized_url":"https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3"},
				{"label":"B","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"https://acmp.ru/?main=task&id_task=1"}],
			"subcontests":[{"title":"З","task_count":2,"tasks":[
				{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3"},
				{"label":"B","url":"https://acmp.ru/?main=task&id_task=1"}]}],
			"rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["solved","none"]}]}]}`)

	get := func(target, login, password string, handler http.HandlerFunc) string {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("group_name", "g1")
		if login != "" {
			req.SetBasicAuth(login, password)
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", target, rec.Code)
		}
		return rec.Body.String()
	}

	// Ученик (без токена) — клиентские ссылки, судейских быть не должно.
	pub := get("/standings/g1", "", "", h.GroupStandingsPage)
	if !strings.Contains(pub, "new-client?contest_id=25408") {
		t.Error("ученику ejudge-ссылка должна остаться клиентской")
	}
	if strings.Contains(pub, "new-judge") {
		t.Error("ученику судейских ссылок быть не должно")
	}

	// По токену (наблюдатель) и в панели (жюри) — судейские.
	for _, c := range []struct {
		name, target, login, pass string
		handler                   http.HandlerFunc
	}{
		{"токен", "/standings/g1?token=tok", "", "", h.GroupStandingsPage},
		{"панель", "/standings/g1/panel", "j", "jp", h.GroupPanelPage},
	} {
		body := get(c.target, c.login, c.pass, c.handler)
		if !strings.Contains(body, "https://ej.kod-u.ru/new-judge?contest_id=25408") {
			t.Errorf("%s: ожидалась судейская ссылка на задачу", c.name)
		}
		if strings.Contains(body, "new-client") {
			t.Errorf("%s: клиентских ejudge-ссылок остаться не должно", c.name)
		}
		// Материалы контеста — тоже в режиме судьи.
		if !strings.Contains(body, "https://ej.kod-u.ru/new-judge?contest_id=777") {
			t.Errorf("%s: материал ejudge должен открываться в режиме судьи", c.name)
		}
		// Чужие сайты не трогаем.
		if !strings.Contains(body, "acmp.ru/?main=task&amp;id_task=1") {
			t.Errorf("%s: ссылка на acmp должна остаться прежней", c.name)
		}
	}

	// JSON сводной: без токена — клиентские, по токену — судейские.
	summary := func(target string) string {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("group_name", "g1")
		rec := httptest.NewRecorder()
		h.GroupSummaryData(rec, req)
		return rec.Body.String()
	}
	if body := summary("/standings/g1/summary-data"); strings.Contains(body, "new-judge") {
		t.Error("публичная сводная не должна отдавать судейские ссылки")
	}
	if body := summary("/standings/g1/summary-data?token=tok"); !strings.Contains(body, "new-judge?contest_id=25408") {
		t.Error("сводная по токену должна отдавать судейские ссылки")
	}
}
