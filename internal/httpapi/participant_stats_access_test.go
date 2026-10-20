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

// Статистика участников на страницах группы — время решения задач (≈N′ под
// меткой колонки) и плашка «Контесты · Участники» — показывается ровно по праву
// view.participants. Доступ без этого права видит таблицы как ученик: раньше
// оба блока выводились по «есть хоть какое-то право».
func TestParticipantStatsFollowParticipantsPerm(t *testing.T) {
	// Два доступа: с правом на статистику (STATS) и с другим правом (OTHER).
	setup := func(t *testing.T) *Handlers {
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
		writeTestFile(t, filepath.Join(dataDir, "credentials", "global_accesses.json"), `[
			{"id":"st","title":"Статистика","auth":"token","token":"STATS","scope":"all","perms":["view.participants"]},
			{"id":"ot","title":"Другое","auth":"token","token":"OTHER","scope":"all","perms":["view.unfrozen"]}]`)
		task := `{"label":"A","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"n1","weight":42}`
		writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
			"group_slug":"g1","group_title":"Г1",
			"solved_summary_sites":["acmp"],
			"solved_summary":[{"student_id":"s1","public_name":"Иванов И.","solved_count_on_page_sites":1,"total_solved_count":1,"solved_count_by_site":[1]}],
			"contests":[{"id":"c1","title":"К","score_system":"edu",
				"subcontests":[{"title":"Задачи","task_count":1,"tasks":[`+task+`]}],
				"tasks":[`+task+`],
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

	const weightMark = `class="task-weight"`
	const overviewMark = `class="group-overview"`

	t.Run("с правом на статистику", func(t *testing.T) {
		h := setup(t)
		page := get(t, h, h.GroupStandingsPage, "/standings/g1?token=STATS")
		if !strings.Contains(page, weightMark) || !strings.Contains(page, "≈42′") {
			t.Error("на странице группы должно быть время решения задачи")
		}
		if !strings.Contains(page, overviewMark) {
			t.Error("на странице группы должна быть плашка с числом контестов и участников")
		}
		contest := get(t, h, h.GroupContestPage, "/standings/g1/contest?id=c1&token=STATS")
		if !strings.Contains(contest, weightMark) {
			t.Error("на странице контеста должно быть время решения задачи")
		}
		data := get(t, h, h.GroupSummaryData, "/standings/g1/summary-data?token=STATS")
		if !strings.Contains(data, `"weight":42`) {
			t.Error("сводной нужно время задачи в данных")
		}
	})

	for _, tc := range []struct{ name, query string }{
		{"другое право", "?token=OTHER"},
		{"без доступа", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			page := get(t, h, h.GroupStandingsPage, "/standings/g1"+tc.query)
			if strings.Contains(page, weightMark) || strings.Contains(page, "≈42′") {
				t.Error("без view.participants времени задачи на странице быть не должно")
			}
			if strings.Contains(page, overviewMark) {
				t.Error("без view.participants плашки быть не должно")
			}
			sep := "?"
			if tc.query != "" {
				sep = "&"
			}
			contest := get(t, h, h.GroupContestPage, "/standings/g1/contest"+tc.query+sep+"id=c1")
			if strings.Contains(contest, weightMark) {
				t.Error("без view.participants времени задачи на странице контеста быть не должно")
			}
			// Сводная рисуется скриптом — время не должно приходить и в данных.
			data := get(t, h, h.GroupSummaryData, "/standings/g1/summary-data"+tc.query)
			if strings.Contains(data, `"weight"`) {
				t.Errorf("без view.participants время задачи не должно уходить в JSON: %s", data)
			}
			api := get(t, h, h.APIGroupStandings, "/api/groups/g1/standings"+tc.query)
			if strings.Contains(api, `"weight"`) {
				t.Error("без view.participants время задачи не должно уходить в API")
			}
		})
	}

	// Ответ /summary-data кэшируется по виду доступа: доступ без права не
	// должен получить закэшированный ответ доступа с правом (и наоборот).
	t.Run("кэш сводной различает доступы", func(t *testing.T) {
		h := setup(t)
		if data := get(t, h, h.GroupSummaryData, "/standings/g1/summary-data?token=STATS"); !strings.Contains(data, `"weight":42`) {
			t.Fatal("доступ с правом должен получить время задачи")
		}
		if data := get(t, h, h.GroupSummaryData, "/standings/g1/summary-data?token=OTHER"); strings.Contains(data, `"weight"`) {
			t.Error("доступ без права получил закэшированный ответ доступа с правом")
		}
		if data := get(t, h, h.GroupSummaryData, "/standings/g1/summary-data?token=STATS"); !strings.Contains(data, `"weight":42`) {
			t.Error("доступ с правом получил закэшированный ответ доступа без права")
		}
	})
}

// Кнопка «Заполнить кондуит» ведёт в редактор, который требует konduit.fill.
// Доступ с правами на оценки, но без этого права, кнопки видеть не должен —
// раньше она была у любого доступа с панелью и вела в 404.
func TestKonduitButtonFollowsKonduitFillPerm(t *testing.T) {
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
		writeTestFile(t, filepath.Join(dataDir, "contests.json"), `[]`)
		writeTestFile(t, filepath.Join(dataDir, "groups", "g1", "group.json"),
			`{"title":"Г1","student_ids":["s1"],"accesses":[
				{"id":"a","title":"Доступ","auth":"token","token":"TOK","perms":[`+perms+`]}]}`)
		// kond — уже сгенерированный кондуит, fresh — только что созданный.
		writeTestFile(t, filepath.Join(dataDir, "groups", "g1", "contests.json"), `[
			{"id":"kond","title":"Кондуит","score_system":"edu","source_type":"provider",
			 "provider":"manual_table","provider_config":{"task_count":1},"subcontests":[],"update":true},
			{"id":"fresh","title":"Новый","score_system":"edu","source_type":"provider",
			 "provider":"manual_table","provider_config":{"task_count":1},"subcontests":[],"update":true}]`)
		writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
			"group_slug":"g1","group_title":"Г1",
			"contests":[{"id":"kond","title":"Кондуит","score_system":"edu","source_type":"provider",
				"subcontests":[{"title":"Задачи","task_count":1,"tasks":[{"label":"1"}]}],
				"tasks":[{"label":"1"}],
				"rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["none"],"solved_count":0}]}]}`)
		return h
	}
	page := func(t *testing.T, h *Handlers) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/standings/g1?token=TOK", nil)
		req.SetPathValue("group_name", "g1")
		rec := httptest.NewRecorder()
		h.GroupStandingsPage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d", rec.Code)
		}
		return rec.Body.String()
	}

	t.Run("с konduit.fill", func(t *testing.T) {
		body := page(t, setup(t, `"konduit.fill"`))
		if !strings.Contains(body, "/standings/g1/jury-konduit?id=kond") {
			t.Error("у сгенерированного кондуита должна быть кнопка заполнения")
		}
		if !strings.Contains(body, "/standings/g1/jury-konduit?id=fresh") {
			t.Error("у нового кондуита в панели должна быть ссылка на редактор")
		}
	})

	t.Run("оценки без konduit.fill", func(t *testing.T) {
		body := page(t, setup(t, `"grades.manual","contests.manage"`))
		// Сама ссылка, а не подстрока: путь к редактору есть и в скрипте кнопки
		// «Создать кондуит», которая положена по konduit.create.
		if strings.Contains(body, "/standings/g1/jury-konduit?id=") {
			t.Error("без konduit.fill ссылок на редактор кондуита быть не должно")
		}
		if !strings.Contains(body, "/standings/g1/manage/contests") {
			t.Error("остальная панель (управление контестами) должна остаться")
		}
	})
}
