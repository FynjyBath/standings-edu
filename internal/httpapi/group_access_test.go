package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"standings-edu/internal/domain"
)

// Матрица ролей: токен группы даёт только просмотр, операции требуют учёток
// панели, а управление контестами — роли «админ группы».
func TestGroupRoleMatrix(t *testing.T) {
	h, _ := juryTestSetup(t)

	jury := juryRoleToken(h)
	admin := adminRoleToken(h)
	if jury == "" || admin == "" || jury == admin {
		t.Fatalf("role-token'ы ролей должны быть непустыми и различаться: jury=%q admin=%q", jury, admin)
	}

	// Резолв роли: токен группы — наблюдатель, подписи — жюри/админ.
	cases := []struct {
		name       string
		token      string
		roleToken  string
		wantAtMost GroupRole
	}{
		{"без ничего", "", "", RoleGuest},
		{"токен группы", "tok", "", RoleObserver},
		{"чужой токен", "WRONG", "", RoleGuest},
		{"подпись жюри", "", jury, RoleJury},
		{"подпись админа", "", admin, RoleAdmin},
		{"подделка подписи", "", "deadbeef", RoleGuest},
	}
	for _, c := range cases {
		if got := h.groupRole("g1", c.token, c.roleToken); got != c.wantAtMost {
			t.Errorf("%s: роль=%v, ожидалась %v", c.name, got, c.wantAtMost)
		}
	}

	// Подпись одной группы не подходит к другой.
	if got := h.groupRole("g2", "", admin); got != RoleGuest {
		t.Errorf("подпись группы g1 не должна работать в g2: %v", got)
	}

	// Роль жюри не может управлять контестами, админ — может.
	for _, c := range []struct {
		name      string
		roleToken string
		want      int
	}{
		{"жюри", jury, http.StatusForbidden},
		{"админ", admin, http.StatusOK},
	} {
		code, _ := juryPost(t, h.PanelContestAddRef, map[string]any{
			"slug": "g1", "role_token": c.roleToken, "id": "global1",
		})
		if code != c.want {
			t.Errorf("add-ref под ролью %s: code=%d, ожидался %d", c.name, code, c.want)
		}
	}

	// Оценки и их настройка — от роли жюри; наблюдателю недоступны.
	for _, fn := range []http.HandlerFunc{h.PanelGradesSave, h.PanelGradesConfigSave} {
		if code, _ := juryPost(t, fn, map[string]any{"slug": "g1", "role_token": "tok"}); code != http.StatusForbidden {
			t.Errorf("оценки по токену группы должны быть закрыты, code=%d", code)
		}
	}
	code, _ := juryPost(t, h.PanelGradesConfigSave, map[string]any{
		"slug": "g1", "role_token": jury, "round": 1,
		"columns": []map[string]any{{"id": "zachet", "title": "Зачёт", "weight": 1, "type": "manual"}},
	})
	if code != http.StatusOK {
		t.Errorf("жюри должно настраивать таблицу оценок, code=%d", code)
	}
}

// Смена пароля обесценивает выданные раньше role-token'ы.
func TestRoleTokenRotatesOnPasswordChange(t *testing.T) {
	h, _ := juryTestSetup(t)
	old := adminRoleToken(h)
	if h.groupRole("g1", "", old) != RoleAdmin {
		t.Fatal("исходная подпись должна работать")
	}

	gf, ok, err := h.readGroupFile("g1")
	if err != nil || !ok {
		t.Fatal("group not found")
	}
	gf.PanelAccess.Admin = &domain.GroupPanelCredential{Login: "a", Password: "new-pass"}
	if err := h.writeGroupFile("g1", gf); err != nil {
		t.Fatal(err)
	}
	if got := h.groupRole("g1", "", old); got != RoleGuest {
		t.Fatalf("после смены пароля старая подпись должна перестать работать: %v", got)
	}
	if code, _ := juryPost(t, h.PanelContestAddRef, map[string]any{
		"slug": "g1", "role_token": old, "id": "global1",
	}); code != http.StatusForbidden {
		t.Errorf("операция со старой подписью: code=%d, ожидался 403", code)
	}
}

// Вход в панель: без учёток — 401 с челленджем, с верными — роль и подпись.
func TestPanelLogin(t *testing.T) {
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
	if strings.Contains(body, "/panel/contests") {
		t.Error("жюри не должно видеть ссылку на управление контестами")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("страницы панели не должны кэшироваться")
	}

	// Админ группы: появляется управление контестами.
	rec = panelGet(t, h.GroupPanelPage, "/standings/g1/panel", "g1", "a", "ap")
	if !strings.Contains(rec.Body.String(), "/panel/contests") {
		t.Error("админ группы должен видеть управление контестами")
	}

	// Страница управления контестами: жюри — 403, админ — 200.
	if rec := panelGet(t, h.GroupPanelContestsPage, "/standings/g1/panel/contests", "g1", "j", "jp"); rec.Code != http.StatusForbidden {
		t.Errorf("контесты для жюри: code=%d, ожидался 403", rec.Code)
	}
	if rec := panelGet(t, h.GroupPanelContestsPage, "/standings/g1/panel/contests", "g1", "a", "ap"); rec.Code != http.StatusOK {
		t.Errorf("контесты для админа: code=%d, ожидался 200", rec.Code)
	}
}

// Наблюдатель (только токен) видит полные таблицы, но без панельных блоков.
func TestObserverSeesNoPanel(t *testing.T) {
	h, _ := juryTestSetup(t)

	rec := panelGet(t, h.GroupStandingsPage, "/standings/g1?token=tok", "g1", "", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("страница по токену: code=%d", rec.Code)
	}
	if strings.Contains(body, "Панель группы") {
		t.Error("наблюдатель не должен видеть панель")
	}
	// И никаких приглашений в панель: вход туда — по прямой ссылке от админа.
	if strings.Contains(body, "/panel") {
		t.Error("на странице по токену не должно быть ссылок на панель")
	}
}
