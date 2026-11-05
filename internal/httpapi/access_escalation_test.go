package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/storage"
)

// escalationSetup — группа g1 с доступом-ссылкой «Админ», стоящим первым, и
// входами по паролю с малыми правами. Глобальный куратор — только каталог.
func escalationSetup(t *testing.T) *Handlers {
	t.Helper()
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	genDir := t.TempDir()
	h.loader = storage.NewGeneratedLoader(genDir)
	writeTestFile(t, filepath.Join(dataDir, "students.json"),
		`[{"id":"s1","full_name":"Иванов Иван","public_name":"Иванов И."}]`)
	writeTestFile(t, filepath.Join(dataDir, "groups", "g1", "group.json"), `{"title":"Альфа","student_ids":["s1"],
		"grades":{"columns":[{"id":"kr","title":"КР","weight":1,"type":"manual"}]},
		"accesses":[
			{"id":"adm","title":"Админ","auth":"token","token":"ADMINTOKEN","perms":["view.participants","members.manage","grades.manual"]},
			{"id":"obs","title":"Наблюдатель","auth":"token","token":"OBSTOKEN","perms":["view.participants"]},
			{"id":"st","title":"Статистика","auth":"password","login":"stat","password":"stat-pw","perms":["view.participants"]},
			{"id":"ex","title":"Экспорт","auth":"password","login":"exp","password":"exp-pw","perms":["view.export"]},
			{"id":"ad","title":"Админ-вход","auth":"password","login":"boss","password":"boss-pw","perms":["view.participants","members.manage","grades.manual"]}]}`)
	writeTestFile(t, filepath.Join(dataDir, "credentials", "global_accesses.json"), `[
		{"id":"gd","title":"Каталог","auth":"password","login":"gdir","password":"gdir-pw","scope":"groups","groups":[],"perms":["view.directory"]}]`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{"group_slug":"g1","group_title":"Альфа",
		"grades":{"columns":[{"title":"КР","weight":1}],"rows":[{"student_id":"s1","public_name":"Иванов И.","values":[7],"final":7}]},
		"contests":[{"id":"c1","title":"К","score_system":"edu",
			"tasks":[{"label":"A","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"n1"}],
			"subcontests":[{"title":"З","task_count":1,"tasks":[{"label":"A","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"n1"}]}],
			"rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["solved"],"solved_count":1}]}]}`)
	writeTestFile(t, filepath.Join(genDir, "students", "s1.json"), `{"student_id":"s1","public_name":"Иванов И."}`)
	return h
}

// signIn — вход на дверь группы; дальше браузер шлёт только куку.
func signIn(t *testing.T, h *Handlers, login, password string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/standings/g1/panel", nil)
	req.SetPathValue("group_name", "g1")
	req.SetBasicAuth(login, password)
	rec := httptest.NewRecorder()
	h.GroupPanelPage(rec, req)
	cookie := sessionCookie(rec)
	if rec.Code != http.StatusOK || cookie == nil {
		t.Fatalf("вход %s: code=%d cookie=%v", login, rec.Code, cookie)
	}
	return cookie
}

func cookieGet(t *testing.T, handler http.HandlerFunc, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("group_name", "g1")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// Вошедшему по паролю чужой токен в страницы не подставляется: иначе вход с
// малыми правами находил в разметке токен первого доступа группы и получал его
// права. ФИО при этом остаются ссылками на профиль и открывают его по куке.
func TestSignedInPagesCarryNoForeignToken(t *testing.T) {
	h := escalationSetup(t)
	for _, login := range [][2]string{{"stat", "stat-pw"}, {"exp", "exp-pw"}} {
		cookie := signIn(t, h, login[0], login[1])
		for name, page := range map[string]struct {
			handler http.HandlerFunc
			target  string
		}{
			"группа":  {h.GroupStandingsPage, "/standings/g1"},
			"контест": {h.GroupContestPage, "/standings/g1/contest?id=c1"},
			"сводная": {h.GroupSummaryAllPage, "/standings/g1/summary"},
			"оценки":  {h.GroupGradesPage, "/standings/g1/grades"},
		} {
			rec := cookieGet(t, page.handler, page.target, cookie)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s/%s: code=%d", login[0], name, rec.Code)
			}
			body := rec.Body.String()
			if strings.Contains(body, "ADMINTOKEN") || strings.Contains(body, "OBSTOKEN") {
				t.Errorf("%s/%s: в странице чужой токен", login[0], name)
			}
			if strings.Contains(body, `href="/standings/g1?token=`) {
				t.Errorf("%s/%s: ссылки вошедшего должны идти без токена", login[0], name)
			}
		}
	}

	// С правом на статистику ФИО — ссылка без токена, и по куке она открывается.
	cookie := signIn(t, h, "stat", "stat-pw")
	const profileLink = `href="/standings/g1/student?id=s1"`
	for name, page := range map[string]struct {
		handler http.HandlerFunc
		target  string
	}{
		"группа":    {h.GroupStandingsPage, "/standings/g1"},
		"контест":   {h.GroupContestPage, "/standings/g1/contest?id=c1"},
		"оценки":    {h.GroupGradesPage, "/standings/g1/grades"},
		"участники": {h.GroupParticipantsPage, "/standings/g1/participants"},
	} {
		if body := cookieGet(t, page.handler, page.target, cookie).Body.String(); !strings.Contains(body, profileLink) {
			t.Errorf("%s: ФИО должно вести на профиль", name)
		}
	}
	if body := cookieGet(t, h.GroupSummaryAllPage, "/standings/g1/summary", cookie).Body.String(); !strings.Contains(body, "var canViewProfiles = true") {
		t.Error("сводная должна рисовать ФИО ссылками")
	}
	rec := cookieGet(t, h.GroupStudentProfilePage, "/standings/g1/student?id=s1", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Иванов И.") {
		t.Errorf("профиль по куке: code=%d", rec.Code)
	}

	// Права вошедшего — ровно его: управление составом закрыто.
	if code := cookieGet(t, h.GroupManageMembersPage, "/standings/g1/manage/members", cookie).Code; code != http.StatusForbidden {
		t.Errorf("управление составом без права: code=%d, want 403", code)
	}
	// Пришедший по своей ссылке токен в ссылках сохраняет.
	body := cookieGet(t, h.GroupStandingsPage, "/standings/g1?token=OBSTOKEN", nil).Body.String()
	if !strings.Contains(body, "/standings/g1/student?id=s1&token=OBSTOKEN") {
		t.Error("у пришедшего по ссылке токен должен оставаться в ссылках")
	}
	if strings.Contains(body, "ADMINTOKEN") {
		t.Error("пришедший по ссылке наблюдателя не должен видеть токен админа")
	}
}

// Каталог показывает только ссылки, не шире прав смотрящего в этой группе.
func TestDirectoryLinksNotWiderThanViewer(t *testing.T) {
	h := escalationSetup(t)
	index := func(cookie *http.Cookie, target string) string {
		t.Helper()
		rec := cookieGet(t, h.IndexPage, target, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", target, rec.Code)
		}
		return rec.Body.String()
	}
	check := func(who, body string, wantObs, wantAdmin bool) {
		t.Helper()
		if !strings.Contains(body, `href="/standings/g1"`) {
			t.Errorf("%s: обычный адрес группы должен быть", who)
		}
		if got := strings.Contains(body, "OBSTOKEN"); got != wantObs {
			t.Errorf("%s: ссылка наблюдателя видна=%v, want %v", who, got, wantObs)
		}
		if got := strings.Contains(body, "ADMINTOKEN"); got != wantAdmin {
			t.Errorf("%s: ссылка админа видна=%v, want %v", who, got, wantAdmin)
		}
	}
	check("экспорт", index(signIn(t, h, "exp", "exp-pw"), "/standings"), false, false)
	check("статистика", index(signIn(t, h, "stat", "stat-pw"), "/standings"), true, false)
	check("админ-вход", index(signIn(t, h, "boss", "boss-pw"), "/standings"), true, true)
	check("ссылка наблюдателя", index(nil, "/standings?token=OBSTOKEN"), true, false)
	check("ссылка админа", index(nil, "/standings?token=ADMINTOKEN"), true, true)

	// Право каталога вне своей области ссылок с токеном не открывает.
	req := httptest.NewRequest(http.MethodGet, "/standings", nil)
	req.SetBasicAuth("gdir", "gdir-pw")
	rec := httptest.NewRecorder()
	h.IndexPage(rec, req)
	check("каталог", rec.Body.String(), false, false)
}
