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

// filterTestHandlers — сервер поверх заданных generated-таблиц.
func filterTestHandlers(t *testing.T, standingsJSON string) *Handlers {
	t.Helper()
	dataDir := t.TempDir()
	genDir := t.TempDir()
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
		`{"title":"Г1","student_ids":["s1","s2"]}`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), standingsJSON)
	return h
}

func groupPageBody(t *testing.T, h *Handlers) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/standings/g1", nil)
	req.SetPathValue("group_name", "g1")
	rec := httptest.NewRecorder()
	h.GroupStandingsPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("страница группы: code=%d", rec.Code)
	}
	return rec.Body.String()
}

// Фильтр по ученику: панель со списком всех учеников страницы и строки,
// помеченные учеником, — за них цепляется фильтр (см. app.js).
func TestGroupPageStudentFilterMarkup(t *testing.T) {
	h := filterTestHandlers(t, `{
		"group_slug":"g1","group_title":"Г1",
		"solved_summary_sites":["acmp"],
		"solved_summary":[
			{"student_id":"s2","public_name":"Яковлев Я.","solved_count_on_page_sites":1,"total_solved_count":1,"solved_count_by_site":[1]},
			{"student_id":"s1","public_name":"Иванов И.","solved_count_on_page_sites":2,"total_solved_count":2,"solved_count_by_site":[2]}],
		"contests":[{
			"id":"c1","title":"Контест","score_system":"edu",
			"tasks":[{"label":"A","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"https://acmp.ru/?main=task&id_task=1"}],
			"subcontests":[{"title":"З","task_count":1,"tasks":[{"label":"A","url":"https://acmp.ru/?main=task&id_task=1"}]}],
			"rows":[
				{"student_id":"s1","public_name":"Иванов И.","statuses":["solved"],"solved_count":1},
				{"student_id":"s2","public_name":"Яковлев Я.","statuses":["none"],"solved_count":0}]}]}`)

	body := groupPageBody(t, h)

	for _, want := range []string{
		"data-student-filter",       // панель
		"data-student-filter-input", // поле ввода
		"data-student-filter-reset", // сброс
		"standingsLoadAllLazy",      // хук догрузки отложенных таблиц
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на странице нет %q", want)
		}
	}

	// Список для автодополнения — по алфавиту и без повторов, даже если ученик
	// встречается и в доске почёта, и в контесте.
	iv := strings.Index(body, `<option value="Иванов И."></option>`)
	ya := strings.Index(body, `<option value="Яковлев Я."></option>`)
	if iv < 0 || ya < 0 {
		t.Fatalf("в списке автодополнения нет учеников: Иванов=%d Яковлев=%d", iv, ya)
	}
	if iv > ya {
		t.Error("список учеников должен быть по алфавиту")
	}
	if n := strings.Count(body, `<option value="Иванов И."></option>`); n != 1 {
		t.Errorf("ученик в списке %d раз, ожидался один", n)
	}

	// Строки таблиц помечены учеником: и в контесте, и в доске почёта.
	for _, want := range []string{
		`data-student="s1" data-filter-text="Иванов И."`,
		`data-student="s2" data-filter-text="Яковлев Я."`,
	} {
		if n := strings.Count(body, want); n < 2 {
			t.Errorf("строка %q встречается %d раз, ожидалось минимум 2 (контест и доска почёта)", want, n)
		}
	}
}

// Пустой группе фильтр не нужен — панели быть не должно.
func TestGroupPageStudentFilterHiddenWithoutStudents(t *testing.T) {
	h := filterTestHandlers(t, `{"group_slug":"g1","group_title":"Г1","contests":[]}`)
	if body := groupPageBody(t, h); strings.Contains(body, "data-student-filter") {
		t.Error("без учеников панель фильтра показывать незачем")
	}
}
