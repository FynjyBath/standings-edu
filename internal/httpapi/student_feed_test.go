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

// В ленте посылок должно быть видно название задачи рядом с меткой: по «Инф 3036»
// преподаватель не поймёт, о чём речь. Проверяем на отрисованной странице, а не
// на структуре: между профилем и глазами есть ещё шаблон.
func TestStudentPageShowsTaskNameInFeed(t *testing.T) {
	genDir := t.TempDir()
	h := NewHandlers(
		storage.NewGeneratedLoader(genDir), nil,
		web.NewTemplateRenderer(filepath.Join("..", "..", "web", "templates")),
		log.New(io.Discard, "", 0),
	)
	if err := h.ConfigureAdmin(AdminConfig{
		Login: "admin", Password: "pw", ProjectRoot: t.TempDir(), DataDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(genDir, "students", "s1.json"), `{
		"student_id":"s1","public_name":"Третьяков Е. М.",
		"stats":{"total_solved":1,"total_submissions":2},
		"recent":[
			{"at":"2026-09-25T13:33:14Z","site":"informatics",
			 "task_url":"https://informatics.mccme.ru/mod/statements/view.php?chapterid=3036",
			 "label":"Инф 3036","name":"P-base","solved":false},
			{"at":"2026-09-25T18:23:00Z","site":"informatics",
			 "task_url":"https://informatics.mccme.ru/mod/statements/view.php?chapterid=39",
			 "label":"Инф 39","solved":false}
		]}`)

	req := httptest.NewRequest(http.MethodGet, "/standings/admin/student?id=s1", nil)
	rec := httptest.NewRecorder()
	h.AdminStudentProfilePage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "P-base") {
		t.Error("на странице нет названия задачи")
	}
	if !strings.Contains(body, "Инф 3036") {
		t.Error("метка задачи должна остаться")
	}
	// Задача без названия не должна ломать строку.
	if !strings.Contains(body, "Инф 39") {
		t.Error("посылка по задаче без названия пропала")
	}
	// Фильтр по тексту должен находить задачу и по названию.
	if !strings.Contains(body, `data-filter-text="Informatics Инф 3036 P-base`) {
		t.Error("название не попало в текст для фильтра")
	}
}
