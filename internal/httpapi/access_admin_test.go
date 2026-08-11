package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
)

// Сохранение доступов группы: токен генерируется сам, право каталога локально
// запрещено, а старые поля вытесняются первой же записью.
func TestAdminGroupAccessesSave(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	writeTestFile(t, filepath.Join(dataDir, "groups", "grp", "group.json"),
		`{"title":"Т","student_ids":["s1"],"group_secret_token":"old-tok",
		  "panel_access":{"admin":{"login":"a","password":"ap"}}}`)

	// Право каталога — только у глобальных доступов.
	code, resp := postJSON(t, h.AdminGroupAccessesSave,
		`{"slug":"grp","accesses":[{"title":"К","auth":"token","perms":["view.directory"]}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("право каталога у группы: code=%d resp=%v", code, resp)
	}
	// Без прав запись бессмысленна.
	if code, _ := postJSON(t, h.AdminGroupAccessesSave,
		`{"slug":"grp","accesses":[{"title":"К","auth":"token","perms":[]}]}`); code != http.StatusBadRequest {
		t.Errorf("доступ без прав: code=%d, ожидался 400", code)
	}
	// Два одинаковых логина — почти наверняка опечатка.
	if code, _ := postJSON(t, h.AdminGroupAccessesSave, `{"slug":"grp","accesses":[
		{"title":"A","auth":"password","login":"x","password":"1","perms":["view.unfrozen"]},
		{"title":"B","auth":"password","login":"x","password":"2","perms":["view.unfrozen"]}]}`); code != http.StatusBadRequest {
		t.Errorf("дубль логина: code=%d, ожидался 400", code)
	}

	code, resp = postJSON(t, h.AdminGroupAccessesSave,
		`{"slug":"grp","accesses":[{"title":"Жюри","auth":"password","login":"j","password":"jp","perms":["grades.manual","view.unfrozen"]}]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("сохранение: code=%d resp=%v", code, resp)
	}

	gf, ok, err := h.readGroupFile("grp")
	if err != nil || !ok {
		t.Fatal("group not found")
	}
	if len(gf.Accesses) != 1 || gf.Accesses[0].ID == "" {
		t.Fatalf("доступ не сохранён: %+v", gf.Accesses)
	}
	// Легаси вытеснено: старые токен и учётка больше не работают.
	if gf.GroupSecretToken != "" || gf.PanelAccess != nil {
		t.Fatalf("старые поля должны исчезнуть: %+v", gf)
	}
	req := httptest.NewRequest(http.MethodGet, "/standings/grp?token=old-tok", nil)
	if h.resolveAccess("grp", req).Elevated() {
		t.Error("старый токен должен перестать действовать после переноса")
	}
}

// Глобальные доступы: область «выбранные группы» без групп — ошибка, если это
// не чистый каталог; сохранённое видно резолверу сразу.
func TestAdminGlobalAccessesSave(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	writeTestFile(t, filepath.Join(dataDir, "groups", "grp", "group.json"), `{"title":"Т","student_ids":[]}`)

	if code, _ := postJSON(t, h.AdminGlobalAccessesSave, `{"accesses":[
		{"title":"Пусто","auth":"token","token":"t","scope":"groups","groups":[],"perms":["view.unfrozen"]}]}`); code != http.StatusBadRequest {
		t.Errorf("область без групп: code=%d, ожидался 400", code)
	}

	code, resp := postJSON(t, h.AdminGlobalAccessesSave, `{"accesses":[
		{"title":"Куратор","auth":"token","token":"gtok","scope":"all","perms":["view.directory","view.unfrozen"]}]}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("сохранение: code=%d resp=%v", code, resp)
	}
	req := httptest.NewRequest(http.MethodGet, "/standings/grp?token=gtok", nil)
	if !h.resolveAccess("grp", req).Has(domain.PermViewUnfrozen) {
		t.Error("глобальный доступ должен действовать в группе сразу после сохранения")
	}
}

// Каталог: без права — приветственный экран, с правом — только покрытые группы
// и только ссылки с токеном.
func TestIndexDirectoryByGlobalAccess(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	writeTestFile(t, filepath.Join(dataDir, "groups", "one", "group.json"),
		`{"title":"Первая","student_ids":[],"accesses":[
		  {"id":"o","title":"Наблюдатель","auth":"token","token":"tok1","perms":["view.unfrozen"]},
		  {"id":"p","title":"Жюри","auth":"password","login":"j","password":"jp","perms":["grades.manual"]}]}`)
	writeTestFile(t, filepath.Join(dataDir, "groups", "two", "group.json"),
		`{"title":"Вторая","student_ids":[],"accesses":[
		  {"id":"o2","title":"Наблюдатель","auth":"token","token":"tok2","perms":["view.unfrozen"]}]}`)
	if err := h.saveGlobalAccesses([]domain.AccessEntry{{
		ID: "dir", Title: "Каталог", Auth: domain.AccessAuthToken, Token: "dirtok",
		Scope: domain.AccessScopeGroups, Groups: []string{"one"},
		Perms: []domain.Perm{domain.PermViewDirectory},
	}}); err != nil {
		t.Fatal(err)
	}

	body := indexBody(t, h, "/standings")
	if strings.Contains(body, "Все группы") {
		t.Error("без права каталога должен быть обычный экран")
	}

	body = indexBody(t, h, "/standings?token=dirtok")
	if !strings.Contains(body, "Первая") {
		t.Fatal("каталог должен показывать покрытую группу")
	}
	if strings.Contains(body, "Вторая") {
		t.Error("группа вне области доступа не должна попадать в каталог")
	}
	if !strings.Contains(body, "token=tok1") {
		t.Error("нужна ссылка с токеном доступа группы")
	}
	// И обычный адрес группы: его рассылают ученикам, а вошедшему преподавателю
	// он открывает группу под его правами.
	if !strings.Contains(body, `href="/standings/one"`) {
		t.Error("нужна обычная ссылка на группу")
	}
	if strings.Contains(body, "jp") {
		t.Error("пароли доступов в каталог попадать не должны")
	}
}

func indexBody(t *testing.T, h *Handlers, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.IndexPage(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: code=%d", target, rec.Code)
	}
	return rec.Body.String()
}

// Журнал: вход и изменение пишутся, чтение — нет; страница журнала их показывает.
func TestAuditLogWritesAndPage(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	// Вход по паролю и удачное изменение.
	panelGet(t, h.GroupPanelPage, "/standings/g1/panel", "g1", "j", "jp")
	if code, _ := juryPost(t, h.PanelGradesSave, tokJury, map[string]any{
		"slug": "g1", "grades": map[string]map[string]float64{"activity": {"s1": 5}},
	}); code != http.StatusOK {
		t.Fatal("оценки должны сохраниться")
	}
	// Неудачный вход тоже интересен.
	panelGet(t, h.GroupPanelPage, "/standings/g1/panel", "g1", "j", "WRONG")
	// Чтение страницы группы в журнал не идёт.
	accessGet(t, h.GroupStandingsPage, "/standings/g1?token="+tokObserver, "g1")

	blob, err := os.ReadFile(filepath.Join(dataDir, "logs", "audit.log"))
	if err != nil {
		t.Fatalf("журнал не создан: %v", err)
	}
	log := string(blob)
	for _, want := range []string{`"access.signin"`, `"grades.manual.save"`, `"ok":false`} {
		if !strings.Contains(log, want) {
			t.Errorf("в журнале нет %s: %s", want, log)
		}
	}
	if strings.Contains(log, "standings.view") {
		t.Error("чтение логировать не нужно")
	}

	rec := httptest.NewRecorder()
	h.AdminLogsPage(rec, httptest.NewRequest(http.MethodGet, "/standings/admin/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("страница журнала: code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "grades.manual.save") {
		t.Error("страница журнала должна показывать записи")
	}
}

// Дверь глобального входа: без учётки — челлендж, с верной — сессия и каталог,
// а если глобальных доступов по паролю нет — двери нет вовсе.
func TestGlobalSignIn(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	writeTestFile(t, filepath.Join(dataDir, "groups", "one", "group.json"),
		`{"title":"Первая","student_ids":[],"accesses":[
		  {"id":"o","title":"Наблюдатель","auth":"token","token":"tok1","perms":["view.unfrozen"]}]}`)

	get := func(login, password string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/standings/login", nil)
		if login != "" {
			req.SetBasicAuth(login, password)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.GlobalSignIn(rec, req)
		return rec
	}

	// Глобальных доступов с паролем нет — двери тоже нет.
	if rec := get("", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("без парольных доступов: code=%d, ожидался 404", rec.Code)
	}

	if err := h.saveGlobalAccesses([]domain.AccessEntry{{
		ID: "cur", Title: "Куратор", Auth: domain.AccessAuthPassword, Login: "cur", Password: "cp",
		Scope: domain.AccessScopeAll,
		Perms: []domain.Perm{domain.PermViewDirectory, domain.PermViewUnfrozen},
	}}); err != nil {
		t.Fatal(err)
	}

	rec := get("", "", nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `realm="standings"`) {
		t.Fatalf("без логина ожидался челлендж: code=%d auth=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if rec := get("cur", "WRONG", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("неверный пароль: code=%d, ожидался 401", rec.Code)
	}

	rec = get("cur", "cp", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/standings" {
		t.Fatalf("после входа ожидался редирект в каталог: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	session := sessionCookie(rec)
	if session == nil {
		t.Fatal("после входа должна выдаваться кука сессии")
	}

	// По сессии каталог открывается без повторного ввода пароля.
	req := httptest.NewRequest(http.MethodGet, "/standings", nil)
	req.AddCookie(session)
	page := httptest.NewRecorder()
	h.IndexPage(page, req)
	body := page.Body.String()
	if !strings.Contains(body, "Первая") || !strings.Contains(body, "/standings/signout") {
		t.Fatal("каталог по сессии не открылся")
	}
	// Вошедшему объясняем, что ссылки открываются с его правами.
	if !strings.Contains(body, "Вы вошли по логину и паролю") {
		t.Error("нужна подсказка про вход")
	}
	// Ссылка «Вход для преподавателей» показывается только на пустом экране.
	anon := httptest.NewRecorder()
	h.IndexPage(anon, httptest.NewRequest(http.MethodGet, "/standings", nil))
	if !strings.Contains(anon.Body.String(), "/standings/login") {
		t.Error("на приветственном экране нужна ссылка входа")
	}
}
