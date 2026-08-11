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

// accessGet — GET с токеном доступа в адресе.
func accessGet(t *testing.T, handler http.HandlerFunc, target, slug string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("group_name", slug)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// sessionCookie — кука сессии из ответа (nil — не выдана).
func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == accessCookieName {
			return c
		}
	}
	return nil
}

// Матрица прав: наблюдатель ничего не меняет, жюри правит оценки, но не
// контесты, админ группы — и контесты тоже.
func TestAccessPermMatrix(t *testing.T) {
	h, _ := juryTestSetup(t)

	gradesConfig := map[string]any{
		"slug": "g1", "round": 1,
		"columns": []map[string]any{{"id": "zachet", "title": "Зачёт", "weight": 1, "type": "manual"}},
	}
	cases := []struct {
		name    string
		token   string
		handler http.HandlerFunc
		body    map[string]any
		want    int
	}{
		{"наблюдатель: контесты", tokObserver, h.PanelContestAddRef, map[string]any{"slug": "g1", "id": "global1"}, http.StatusForbidden},
		{"наблюдатель: оценки", tokObserver, h.PanelGradesConfigSave, gradesConfig, http.StatusForbidden},
		{"жюри: контесты", tokJury, h.PanelContestAddRef, map[string]any{"slug": "g1", "id": "global1"}, http.StatusForbidden},
		{"жюри: оценки", tokJury, h.PanelGradesConfigSave, gradesConfig, http.StatusOK},
		{"админ: контесты", tokAdmin, h.PanelContestAddRef, map[string]any{"slug": "g1", "id": "global1"}, http.StatusOK},
		{"чужой токен", "WRONG", h.PanelGradesConfigSave, gradesConfig, http.StatusForbidden},
		{"без токена", "", h.PanelGradesConfigSave, gradesConfig, http.StatusForbidden},
	}
	for _, c := range cases {
		if code, _ := juryPost(t, c.handler, c.token, c.body); code != c.want {
			t.Errorf("%s: code=%d, ожидался %d", c.name, code, c.want)
		}
	}

	// Токен одной группы не действует в другой.
	if code, _ := juryPost(t, h.PanelContestAddRef, tokAdmin, map[string]any{"slug": "g2", "id": "global1"}); code != http.StatusForbidden {
		t.Errorf("токен g1 не должен работать в g2: code=%d", code)
	}
}

// Выключенный доступ не даёт ничего — ни просмотра, ни операций.
func TestAccessDisabledEntry(t *testing.T) {
	h, _ := juryTestSetup(t)

	gf, ok, err := h.readGroupFile("g1")
	if err != nil || !ok {
		t.Fatal("group not found")
	}
	off := false
	for i := range gf.Accesses {
		if gf.Accesses[i].ID == "adm" {
			gf.Accesses[i].Enabled = &off
		}
	}
	if err := h.writeGroupFile("g1", gf); err != nil {
		t.Fatal(err)
	}
	if code, _ := juryPost(t, h.PanelContestAddRef, tokAdmin, map[string]any{"slug": "g1", "id": "global1"}); code != http.StatusForbidden {
		t.Errorf("выключенный доступ: code=%d, ожидался 403", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/standings/g1?token="+tokAdmin, nil)
	if acc := h.resolveAccess("g1", req); acc.Elevated() {
		t.Error("выключенный доступ не должен давать прав на просмотр")
	}
}

// Права нескольких подтверждённых доступов объединяются: глобальный доступ на
// все группы действует и там, где у группы своих прав нет.
func TestAccessUnionWithGlobal(t *testing.T) {
	h, _ := juryTestSetup(t)

	if err := h.saveGlobalAccesses([]domain.AccessEntry{{
		ID: "curator", Title: "Куратор", Auth: domain.AccessAuthToken, Token: "gtok",
		Scope: domain.AccessScopeAll,
		Perms: []domain.Perm{domain.PermViewDirectory, domain.PermContestsManage, domain.PermContestsGlobal},
	}}); err != nil {
		t.Fatal(err)
	}

	// Глобальный токен сам по себе даёт управление контестами в любой группе.
	if code, _ := juryPost(t, h.PanelContestAddRef, "gtok", map[string]any{"slug": "g1", "id": "global1"}); code != http.StatusOK {
		t.Errorf("глобальный доступ должен работать в группе: code=%d", code)
	}
	// А у локального жюри своих контестных прав по-прежнему нет.
	req := httptest.NewRequest(http.MethodGet, "/standings/g1?token="+tokJury, nil)
	acc := h.resolveAccess("g1", req)
	if !acc.Has(domain.PermGradesManual) || acc.Has(domain.PermContestsManage) {
		t.Fatalf("жюри без глобального: %v", acc.Perms.Sorted())
	}
}

// Область действия глобального доступа: только перечисленные группы.
func TestGlobalAccessScopeGroups(t *testing.T) {
	h, _ := juryTestSetup(t)
	if err := h.saveGlobalAccesses([]domain.AccessEntry{{
		ID: "one", Title: "Только g2", Auth: domain.AccessAuthToken, Token: "gtok",
		Scope: domain.AccessScopeGroups, Groups: []string{"g2"},
		Perms: []domain.Perm{domain.PermContestsManage},
	}}); err != nil {
		t.Fatal(err)
	}
	if code, _ := juryPost(t, h.PanelContestAddRef, "gtok", map[string]any{"slug": "g1", "id": "global1"}); code != http.StatusForbidden {
		t.Errorf("доступ вне области не должен работать: code=%d", code)
	}
}

// Вход по логину и паролю: без учётки — 401 с челленджем, после входа выдаётся
// кука сессии, по ней права работают уже без пароля, а «Выйти» её гасит.
func TestAccessSignInSession(t *testing.T) {
	h, _ := juryTestSetup(t)

	rec := panelGet(t, h.GroupPanelPage, "/standings/g1/panel", "g1", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("без логина ожидался 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), `realm="group-g1"`) {
		t.Fatalf("челлендж должен быть со своим realm группы: %q", rec.Header().Get("WWW-Authenticate"))
	}

	// Жюри: страница открывается, панель есть, управления контестами нет.
	rec = panelGet(t, h.GroupPanelPage, "/standings/g1/panel", "g1", "j", "jp")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Панель группы") {
		t.Fatalf("жюри должно видеть панель: code=%d", rec.Code)
	}
	if strings.Contains(body, "/manage/contests") {
		t.Error("жюри не должно видеть ссылку на управление контестами")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("страницы под доступом не должны кэшироваться")
	}

	// Кука сессии выдана — по ней права работают без Basic Auth.
	session := sessionCookie(rec)
	if session == nil || session.Value == "" {
		t.Fatal("после входа должна выдаваться кука сессии")
	}
	req := httptest.NewRequest(http.MethodGet, "/standings/g1", nil)
	req.AddCookie(session)
	acc := h.resolveAccess("g1", req)
	if !acc.Has(domain.PermGradesManual) || !acc.SignedIn {
		t.Fatalf("сессия должна давать права жюри: %v", acc.Perms.Sorted())
	}

	// Админ группы: появляется управление контестами.
	rec = panelGet(t, h.GroupPanelPage, "/standings/g1/panel", "g1", "a", "ap")
	if !strings.Contains(rec.Body.String(), "/manage/contests") {
		t.Error("админ группы должен видеть управление контестами")
	}

	// Страница управления контестами: жюри — 403, админ — 200.
	if rec := panelGet(t, h.GroupManageContestsPage, "/standings/g1/manage/contests", "g1", "j", "jp"); rec.Code != http.StatusForbidden {
		t.Errorf("контесты для жюри: code=%d, ожидался 403", rec.Code)
	}
	if rec := panelGet(t, h.GroupManageContestsPage, "/standings/g1/manage/contests", "g1", "a", "ap"); rec.Code != http.StatusOK {
		t.Errorf("контесты для админа: code=%d, ожидался 200", rec.Code)
	}

	// Выход: кука гасится.
	out := httptest.NewRecorder()
	h.AccessSignOut(out, httptest.NewRequest(http.MethodGet, "/standings/signout?back=/standings/g1", nil))
	if out.Code != http.StatusSeeOther {
		t.Fatalf("выход должен редиректить: code=%d", out.Code)
	}
	if c := sessionCookie(out); c == nil || c.MaxAge >= 0 {
		t.Error("кука сессии должна гаситься при выходе")
	}
}

// Смена пароля обесценивает выданные раньше сессии.
func TestSessionDiesOnPasswordChange(t *testing.T) {
	h, _ := juryTestSetup(t)

	session := sessionCookie(panelGet(t, h.GroupPanelPage, "/standings/g1/panel", "g1", "a", "ap"))
	if session == nil {
		t.Fatal("сессия не выдана")
	}

	gf, ok, err := h.readGroupFile("g1")
	if err != nil || !ok {
		t.Fatal("group not found")
	}
	for i := range gf.Accesses {
		if gf.Accesses[i].ID == "adm-pw" {
			gf.Accesses[i].Password = "new-pass"
		}
	}
	if err := h.writeGroupFile("g1", gf); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/standings/g1", nil)
	req.AddCookie(session)
	if acc := h.resolveAccess("g1", req); acc.Elevated() {
		t.Fatalf("после смены пароля старая сессия должна перестать работать: %v", acc.Perms.Sorted())
	}
}

// Токен наблюдателя: полные таблицы видны, но панели и приглашений в неё нет.
func TestObserverSeesNoPanel(t *testing.T) {
	h, _ := juryTestSetup(t)

	rec := accessGet(t, h.GroupStandingsPage, "/standings/g1?token="+tokObserver, "g1")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("страница по токену: code=%d", rec.Code)
	}
	if strings.Contains(body, "Панель группы") {
		t.Error("наблюдатель не должен видеть панель")
	}
	// И никаких приглашений войти: вход — по прямой ссылке от админа.
	if strings.Contains(body, "/panel") {
		t.Error("на странице по токену не должно быть ссылок на вход")
	}
	// Зато есть то, что даёт просмотр: участники и выгрузка.
	if !strings.Contains(body, "/participants") || !strings.Contains(body, "export.xlsx") {
		t.Error("наблюдателю должны быть доступны участники и экспорт")
	}
}

// Легаси-поля (group_secret_token, panel_access) продолжают работать, пока у
// группы нет списка accesses.
func TestLegacyAccessFallback(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	writeTestFile(t, filepath.Join(dataDir, "groups", "old", "group.json"),
		`{"title":"Старая","student_ids":[],"group_secret_token":"legacy-tok",
		  "panel_access":{"admin":{"login":"a","password":"ap"}}}`)

	req := httptest.NewRequest(http.MethodGet, "/standings/old?token=legacy-tok", nil)
	acc := h.resolveAccess("old", req)
	if !acc.Has(domain.PermViewUnfrozen) || acc.Has(domain.PermContestsManage) {
		t.Fatalf("старый токен группы = наблюдатель: %v", acc.Perms.Sorted())
	}

	req = httptest.NewRequest(http.MethodGet, "/standings/old", nil)
	req.SetBasicAuth("a", "ap")
	acc = h.resolveAccess("old", req)
	if !acc.Has(domain.PermContestsManage) {
		t.Fatalf("старая учётка админа должна давать управление контестами: %v", acc.Perms.Sorted())
	}
}

// Право «Доступ ко всем контестам сайта»: без него доступ распоряжается уже
// добавленными контестами группы, но общего списка не видит и из него не
// добавляет. Само определение глобального контеста из группы не меняется.
func TestContestsGlobalPerm(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	// Доступ «завуч»: полное управление записями группы и свои контесты, но без
	// доступа к общему списку сайта.
	gf, ok, err := h.readGroupFile("g1")
	if err != nil || !ok {
		t.Fatal("group not found")
	}
	gf.Accesses = append(gf.Accesses, domain.AccessEntry{
		ID: "local", Title: "Завуч", Auth: domain.AccessAuthToken, Token: "loctok",
		Perms: []domain.Perm{domain.PermViewUnfrozen, domain.PermContestsManage, domain.PermContestsInline},
	})
	if err := h.writeGroupFile("g1", gf); err != nil {
		t.Fatal(err)
	}

	// Добавить глобальный контест нельзя, а свой (inline) — можно.
	if code, resp := juryPost(t, h.PanelContestAddRef, "loctok", map[string]any{"slug": "g1", "id": "global1"}); code != http.StatusForbidden {
		t.Errorf("без права общего списка add-ref: code=%d resp=%v, ожидался 403", code, resp)
	}
	if code, _ := juryPost(t, h.PanelContestInlineSave, "loctok", map[string]any{
		"slug": "g1", "id": "svoy", "title": "Свой", "score_system": "edu",
		"subcontests": []map[string]any{{"title": "З", "tasks": []string{"https://acmp.ru/?main=task&id_task=3"}}},
		"start_time":  "2026-09-01T09:00:00Z", "end_time": "2026-09-01T12:00:00Z",
	}); code != http.StatusOK {
		t.Errorf("свои контесты должны остаться доступны: code=%d", code)
	}
	// Уже добавленными записями (в т.ч. ссылками) распоряжаться можно.
	if code, _ := juryPost(t, h.PanelContestMove, "loctok", map[string]any{"slug": "g1", "id": "other", "dir": "up"}); code != http.StatusOK {
		t.Errorf("переставить запись группы: code=%d, ожидался 200", code)
	}

	// С правом — добавляется.
	if code, _ := juryPost(t, h.PanelContestAddRef, tokAdmin, map[string]any{"slug": "g1", "id": "global1"}); code != http.StatusOK {
		t.Errorf("с правом общего списка add-ref должен работать: code=%d", code)
	}

	// Страница управления: без права нет ни списка сайта, ни выпадашки.
	rec := accessGet(t, h.GroupManageContestsPage, "/standings/g1/manage/contests?token=loctok", "g1")
	if rec.Code != http.StatusOK {
		t.Fatalf("страница контестов: code=%d", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, `id="add-ref-select"`) || strings.Contains(body, "Общий кондуит") {
		t.Error("без права ни выпадашки, ни чужих контестов на странице быть не должно")
	}
	rec = accessGet(t, h.GroupManageContestsPage, "/standings/g1/manage/contests?token="+tokAdmin, "g1")
	if body := rec.Body.String(); !strings.Contains(body, `id="add-ref-select"`) || !strings.Contains(body, "Общий кондуит") {
		t.Error("с правом нужны выпадашка «добавить из глобальных» и сам список")
	}

	// Ссылку на глобальный контест из группы не подменить своим контестом.
	code, resp := juryPost(t, h.PanelContestInlineSave, tokAdmin, map[string]any{
		"slug": "g1", "id": "global1", "original_id": "global1", "title": "Подмена", "score_system": "edu",
		"subcontests": []map[string]any{{"title": "З", "tasks": []string{"https://acmp.ru/?main=task&id_task=4"}}},
		"start_time":  "2026-09-01T09:00:00Z", "end_time": "2026-09-01T12:00:00Z",
	})
	if code != http.StatusForbidden {
		t.Errorf("подмена ссылки на глобальный контест: code=%d resp=%v, ожидался 403", code, resp)
	}
	// Глобальное определение не тронуто.
	blob, err := os.ReadFile(filepath.Join(dataDir, "contests.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "Подмена") {
		t.Fatalf("определение глобального контеста изменилось: %s", blob)
	}
}
