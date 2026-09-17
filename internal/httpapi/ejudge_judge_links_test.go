package httpapi

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
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
				{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3","normalized_url":"https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3","ejudge_site":"kodu","ejudge_prob":"sum-two"},
				{"label":"B","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"https://acmp.ru/?main=task&id_task=1"}],
			"subcontests":[{"title":"З","task_count":2,"tasks":[
				{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3","ejudge_site":"kodu","ejudge_prob":"sum-two"},
				{"label":"B","url":"https://acmp.ru/?main=task&id_task=1"}]}],
			"rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["solved","none"],
				"accounts":{"kodu":"ivanov"}}]}]}`)

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
	if strings.Contains(pub, "ejudge-filter") {
		t.Error("ученику фильтр прогонов показывать незачем")
	}
	if strings.Contains(pub, "ivanov") {
		t.Error("логин ejudge не должен попадать в ученический вид")
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
		// В судейский интерфейс нельзя сослаться на ученика и задачу, поэтому
		// при переходе копируется готовый фильтр прогонов.
		cellFilter := ejudgeAttrFilter(`login == "ivanov" && prob == "sum-two"`)
		if !strings.Contains(body, cellFilter) {
			t.Errorf("%s: у ячейки ученика ожидался фильтр по ученику и задаче", c.name)
		}
		headFilter := ejudgeAttrFilter(`prob == "sum-two"`)
		if !strings.Contains(body, headFilter) {
			t.Errorf("%s: у заголовка колонки ожидался фильтр по задаче", c.name)
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

// ejudgeAttrFilter — как фильтр выглядит в готовом HTML: html/template
// экранирует кавычки и амперсанды в значении атрибута.
func ejudgeAttrFilter(filter string) string {
	escaped := strings.NewReplacer(`&`, "&amp;", `"`, "&#34;").Replace(filter)
	return `data-ejudge-filter="` + escaped + `"`
}

// Логин ejudge нужен только судейскому фильтру и в публичный ответ попадать не
// должен; аккаунт informatics (по нему строится ссылка на посылки ученика,
// видная всем) при этом остаётся. Заодно проверяем, что срезание не портит
// общие с кэшем строки: вид преподавателя после публичного остаётся полным.
func TestEjudgeLoginHiddenFromPublicView(t *testing.T) {
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
		`{"title":"Г1","student_ids":["s1"],"accesses":[
		  {"id":"j","title":"Жюри","auth":"token","token":"jtok",
		   "perms":["view.unfrozen","view.judge_links","view.task_links"]}]}`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
		"group_slug":"g1","group_title":"Г1","contests":[{
			"id":"c1","title":"К","score_system":"edu",
			"tasks":[{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=7&prob_id=1",
				"normalized_url":"https://ej.kod-u.ru/new-client?contest_id=7&prob_id=1",
				"ejudge_site":"kodu","ejudge_prob":"aplusb"}],
			"subcontests":[{"title":"З","task_count":1,"tasks":[
				{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=7&prob_id=1",
				 "ejudge_site":"kodu","ejudge_prob":"aplusb"}]}],
			"rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["solved"],
				"accounts":{"kodu":"ivanov","informatics":"764934"}}]}]}`)

	body := func(token string) string {
		target := "/standings/g1/summary-data"
		if token != "" {
			target += "?token=" + token
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("group_name", "g1")
		rec := httptest.NewRecorder()
		h.GroupSummaryData(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", target, rec.Code)
		}
		return rec.Body.String()
	}

	staff := body("jtok")
	for _, want := range []string{`"ivanov"`, `"aplusb"`, `"kodu"`, "new-judge"} {
		if !strings.Contains(staff, want) {
			t.Errorf("в судейском ответе нет %s", want)
		}
	}

	pub := body("")
	for _, unwanted := range []string{"ivanov", "aplusb", "ejudge_site", "ejudge_prob", "new-judge"} {
		if strings.Contains(pub, unwanted) {
			t.Errorf("в публичном ответе не должно быть %q", unwanted)
		}
	}
	// Аккаунт informatics публичный: по нему ученики открывают свои посылки.
	if !strings.Contains(pub, `"764934"`) {
		t.Error("аккаунт informatics должен остаться в публичном ответе")
	}

	// Публичный вид не должен портить строки, разделяемые с кэшем загрузчика:
	// CloneForServe копирует задачи, но не строки, поэтому срезание логинов
	// обязано пересобирать их, а не править на месте. Проверяем на том уровне,
	// где это видно, — кэш готовых байтов сводной иначе скрыл бы порчу.
	judge := &GroupAccess{Perms: domain.NewPermSet(
		domain.PermViewUnfrozen, domain.PermViewTaskLinks, domain.PermViewJudgeLinks)}
	public := &GroupAccess{Perms: domain.PermSet{}}

	login := func(acc *GroupAccess) string {
		std, err := h.loadGroupStandings("g1")
		if err != nil {
			t.Fatal(err)
		}
		h.applyAccessView(&std, "g1", acc)
		return std.Contests[0].Rows[0].Accounts["kodu"]
	}
	if got := login(judge); got != "ivanov" {
		t.Fatalf("до публичного вида логин судьи = %q, ожидался ivanov", got)
	}
	if got := login(public); got != "" {
		t.Fatalf("в публичном виде логин ejudge = %q, ожидался пустой", got)
	}
	if got := login(judge); got != "ivanov" {
		t.Fatalf("после публичного вида логин судьи = %q — общие строки испорчены", got)
	}
}

// Ячейка «сколько задач из скольки» в сводной ведёт на контест ejudge. Ссылаться
// на ученика там нельзя, поэтому по токену отдаётся судейская ссылка на контест
// и сайт, под которым лежит логин, — из них страница соберёт фильтр по ученику.
// Без токена не должно уезжать ни того, ни другого.
func TestEjudgeContestLinkInSummaryTotals(t *testing.T) {
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
		`{"title":"Г1","student_ids":["s1"],"group_secret_token":"tok"}`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
		"group_slug":"g1","group_title":"Г1","contests":[{
			"id":"c1","title":"Перебор","score_system":"edu",
			"summary_total_only":true,
			"source_url":"https://ej.kod-u.ru/new-client?contest_id=933977",
			"ejudge_site":"kodu",
			"tasks":[{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=933977&prob_id=1","normalized_url":"https://ej.kod-u.ru/new-client?contest_id=933977&prob_id=1","ejudge_site":"kodu","ejudge_prob":"perebor-1"}],
			"subcontests":[{"title":"З","task_count":1,"tasks":[{"label":"A","url":"https://ej.kod-u.ru/new-client?contest_id=933977&prob_id=1","ejudge_site":"kodu","ejudge_prob":"perebor-1"}]}],
			"rows":[{"student_id":"s1","public_name":"Иванов И.","statuses":["solved"],"solved_count":1,
				"accounts":{"kodu":"ivanov","informatics":"12345"}}]}]}`)

	summary := func(target string) string {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("group_name", "g1")
		rec := httptest.NewRecorder()
		h.GroupSummaryData(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", target, rec.Code)
		}
		return rec.Body.String()
	}

	staff := summary("/standings/g1/summary-data?token=tok")
	if !strings.Contains(staff, "new-judge?contest_id=933977") {
		t.Errorf("по токену ссылка на контест должна вести в режим судьи: %s", staff)
	}
	if !strings.Contains(staff, `"ejudge_site":"kodu"`) {
		t.Error("сайт контеста нужен, чтобы собрать фильтр по логину ученика")
	}
	if !strings.Contains(staff, `"kodu":"ivanov"`) {
		t.Error("логин ученика нужен для фильтра")
	}

	pub := summary("/standings/g1/summary-data")
	if strings.Contains(pub, "new-judge") {
		t.Error("публичная сводная не должна вести в режим судьи")
	}
	if strings.Contains(pub, "ivanov") {
		t.Errorf("логин ejudge утёк в публичный ответ: %s", pub)
	}
	if !strings.Contains(pub, `"informatics":"12345"`) {
		t.Error("аккаунт informatics виден всем — его трогать не надо")
	}
	// Ссылка на контест остаётся клиентской: она и ученику полезна.
	if !strings.Contains(pub, "new-client?contest_id=933977") {
		t.Error("клиентская ссылка на контест должна остаться")
	}
}
