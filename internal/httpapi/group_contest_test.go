package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

// contestPageHandlers — группа с тремя контестами, токеном и учёткой жюри.
func contestPageHandlers(t *testing.T) *Handlers {
	t.Helper()
	dataDir, genDir := t.TempDir(), t.TempDir()
	h := NewHandlers(
		storage.NewGeneratedLoader(genDir), nil,
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
		`{"title":"Группа 1","student_ids":["s1"],"group_secret_token":"tok",
		  "panel_access":{"jury":{"login":"j","password":"jp"}}}`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
		"group_slug":"g1","group_title":"Группа 1","contests":[
		 {"id":"c1","title":"Первый","score_system":"edu","table_name":["Тематические"],
		  "tasks":[{"label":"A"}],"subcontests":[{"title":"З","task_count":1,"tasks":[{"label":"A"}]}],
		  "rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["solved"]}]},
		 {"id":"c2","title":"Второй","score_system":"ioi","table_name":["Соревнования"],
		  "tasks":[{"label":"A"}],"subcontests":[{"title":"З","task_count":1,"tasks":[{"label":"A"}]}],
		  "rows":[{"student_id":"s1","public_name":"Иванов И.","total_score":80,"statuses":["solved"],"scores":[80]}]},
		 {"id":"c3","title":"Третий","score_system":"edu",
		  "tasks":[{"label":"A"}],"subcontests":[{"title":"З","task_count":1,"tasks":[{"label":"A"}]}],
		  "rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["none"]}]}]}`)
	return h
}

func contestPageGet(t *testing.T, h *Handlers, handler http.HandlerFunc, target, login, pass string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("group_name", "g1")
	if login != "" {
		req.SetBasicAuth(login, pass)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// Страница одного контеста: только его таблица, крошки, листалка соседей.
func TestGroupContestPage(t *testing.T) {
	h := contestPageHandlers(t)

	rec := contestPageGet(t, h, h.GroupContestPage, "/standings/g1/contest?id=c2", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<h1>Второй") {
		t.Error("нет заголовка контеста")
	}
	// Чужих таблиц на странице быть не должно.
	for _, foreign := range []string{"Первый</h2>", "Третий</h2>"} {
		if strings.Contains(body, foreign) {
			t.Errorf("на странице не должно быть чужой таблицы (%s)", foreign)
		}
	}
	// Листалка ведёт на соседние контесты по порядку группы.
	if !strings.Contains(body, "/contest?id=c1") || !strings.Contains(body, "/contest?id=c3") {
		t.Error("нет ссылок на предыдущий/следующий контест")
	}
	// Публично экспорт не предлагаем.
	if strings.Contains(body, "export.xlsx") {
		t.Error("ученику кнопки Excel быть не должно")
	}

	// Края списка: у первого нет «предыдущего», у последнего — «следующего».
	first := contestPageGet(t, h, h.GroupContestPage, "/standings/g1/contest?id=c1", "", "").Body.String()
	if strings.Contains(first, "Предыдущий") {
		t.Error("у первого контеста не должно быть «предыдущего»")
	}
	last := contestPageGet(t, h, h.GroupContestPage, "/standings/g1/contest?id=c3", "", "").Body.String()
	if strings.Contains(last, "Следующий") {
		t.Error("у последнего контеста не должно быть «следующего»")
	}

	// Несуществующий контест и пустой id — 404.
	for _, target := range []string{"/standings/g1/contest?id=nope", "/standings/g1/contest"} {
		if rec := contestPageGet(t, h, h.GroupContestPage, target, "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: code=%d want 404", target, rec.Code)
		}
	}
}

// По токену — экспорт и токен в ссылках; при входе по паролю — подпись доступа.
func TestGroupContestPageAccess(t *testing.T) {
	h := contestPageHandlers(t)

	withToken := contestPageGet(t, h, h.GroupContestPage, "/standings/g1/contest?id=c2&token=tok", "", "").Body.String()
	if !strings.Contains(withToken, "export.xlsx?token=tok&amp;contest=c2") {
		t.Error("по токену нужна кнопка Excel этой таблицы")
	}
	if !strings.Contains(withToken, "/contest?id=c1&amp;token=tok") {
		t.Error("токен должен переноситься в ссылки листалки")
	}

	// Вход по паролю: та же страница, но с подписью доступа в шапке.
	rec := contestPageGet(t, h, h.GroupContestPage, "/standings/g1/contest?id=c2", "j", "jp")
	if rec.Code != http.StatusOK {
		t.Fatalf("страница под учёткой: code=%d want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "🛠 Жюри") {
		t.Error("нужна подпись доступа")
	}
}

// Экспорт одной таблицы: единственный лист, названный по контесту, без оценок.
func TestExportSingleContestXLSX(t *testing.T) {
	h := contestPageHandlers(t)

	req := httptest.NewRequest(http.MethodGet, "/standings/g1/export.xlsx?token=tok&contest=c2", nil)
	req.SetPathValue("group_name", "g1")
	rec := httptest.NewRecorder()
	h.GroupExportXLSX(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "g1-c2-") {
		t.Errorf("имя файла должно содержать id контеста: %q", rec.Header().Get("Content-Disposition"))
	}
	blob := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("книга должна быть валидным zip: %v", err)
	}
	var workbook string
	for _, f := range zr.File {
		if f.Name == "xl/workbook.xml" {
			rc, _ := f.Open()
			body, _ := io.ReadAll(rc)
			rc.Close()
			workbook = string(body)
		}
	}
	names := regexp.MustCompile(`name="([^"]+)"`).FindAllStringSubmatch(workbook, -1)
	if len(names) != 1 || names[0][1] != "Второй" {
		t.Fatalf("ожидался один лист «Второй», получено: %v", names)
	}

	// Несуществующий контест — 404.
	req = httptest.NewRequest(http.MethodGet, "/standings/g1/export.xlsx?token=tok&contest=nope", nil)
	req.SetPathValue("group_name", "g1")
	rec = httptest.NewRecorder()
	h.GroupExportXLSX(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("несуществующий контест: code=%d want 404", rec.Code)
	}
}
