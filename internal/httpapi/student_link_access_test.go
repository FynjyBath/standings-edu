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

// Ссылка на профиль ученика должна появляться ровно у того доступа, который
// профиль и открывает (view.participants). Проверяем страницы со списками
// учеников: сводная строит таблицу скриптом, остальные — шаблоном.
func TestStudentLinksFollowParticipantsPerm(t *testing.T) {
	setup := func(t *testing.T, perms string) *Handlers {
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
			`{"title":"Г1","student_ids":["s1"]}`)
		writeTestFile(t, filepath.Join(dataDir, "credentials", "global_accesses.json"),
			`[{"id":"a","title":"Доступ","auth":"token","token":"TOK","scope":"all","perms":[`+perms+`]}]`)
		writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
			"group_slug":"g1","group_title":"Г1",
			"contests":[{"id":"c1","title":"К","score_system":"edu",
				"tasks":[{"label":"A","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"n1"}],
				"rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["solved"],"solved_count":1}]}]}`)
		return h
	}

	get := func(t *testing.T, h *Handlers, handler http.HandlerFunc, path string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetPathValue("group_name", "g1")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", path, rec.Code)
		}
		return rec.Body.String()
	}

	t.Run("с правом на участников", func(t *testing.T) {
		h := setup(t, `"view.participants"`)
		list := get(t, h, h.GroupStandingsPage, "/standings/g1?token=TOK")
		if !strings.Contains(list, `/standings/g1/student?id=s1&token=TOK`) {
			t.Error("в таблице списком имя должно вести на профиль")
		}
		sum := get(t, h, h.GroupSummaryAllPage, "/standings/g1/summary?token=TOK")
		if !strings.Contains(sum, "var canViewProfiles = true") {
			t.Error("сводная должна разрешать ссылки на профиль")
		}
	})

	t.Run("другое право, профиль недоступен", func(t *testing.T) {
		h := setup(t, `"view.unfrozen"`)
		list := get(t, h, h.GroupStandingsPage, "/standings/g1?token=TOK")
		if strings.Contains(list, "/standings/g1/student?id=s1") {
			t.Error("без view.participants ссылки быть не должно: профиль отдаёт 404")
		}
		sum := get(t, h, h.GroupSummaryAllPage, "/standings/g1/summary?token=TOK")
		if !strings.Contains(sum, "var canViewProfiles = false") {
			t.Error("сводная не должна разрешать ссылки на профиль")
		}
		// И сам профиль действительно закрыт — ради этого всё и затевалось.
		req := httptest.NewRequest(http.MethodGet, "/standings/g1/student?id=s1&token=TOK", nil)
		req.SetPathValue("group_name", "g1")
		rec := httptest.NewRecorder()
		h.GroupStudentProfilePage(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("профиль без права должен быть недоступен, code=%d", rec.Code)
		}
	})

	t.Run("без токена", func(t *testing.T) {
		h := setup(t, `"view.participants"`)
		list := get(t, h, h.GroupStandingsPage, "/standings/g1")
		if strings.Contains(list, "/standings/g1/student?id=s1") {
			t.Error("ученикам ссылки на профиль не положены")
		}
		if !strings.Contains(list, "Иванов И.") {
			t.Error("имя должно остаться обычным текстом")
		}
	})
}
