package httpapi

import (
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

const childTestSecret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// childLink — ссылка ученика при тестовом секрете.
func childLink(t *testing.T, id string) string {
	t.Helper()
	secret, err := hex.DecodeString(childTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	return "/standings/child?key=" + domain.StudentLinkKey(secret, id)
}

// childTestSetup — ученики s1 (Иванов) и s2 (Петров):
//   - g1 «Альфа»: оба; контесты — где s1 решал (c1), где не делал ничего (c2),
//     скрытый (secret), общий с g2 (shared); есть доска почёта и оценки;
//   - g2 «Бета», архив: только s1; общий контест shared и свой c9;
//   - g3 — объединение g1+g2 (на странице его быть не должно);
//   - g4 «Чужая» — s1 в ней нет.
func childTestSetup(t *testing.T) (*Handlers, string) {
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

	writeTestFile(t, filepath.Join(dataDir, "credentials", "student_links_key.json"), `{"key":"`+childTestSecret+`"}`)
	writeTestFile(t, filepath.Join(dataDir, "students.json"), `[
		{"id":"s1","full_name":"Иванов Иван","public_name":"Иванов И."},
		{"id":"s2","full_name":"Петров Пётр","public_name":"Петров П."}]`)
	writeTestFile(t, filepath.Join(dataDir, "groups", "g1", "group.json"), `{"title":"Альфа","student_ids":["s1","s2"],
		"accesses":[
			{"id":"pl","title":"Родители","auth":"token","token":"PLTOK","perms":["view.parent_links"]},
			{"id":"vp","title":"Статистика","auth":"token","token":"VPTOK","perms":["view.participants"]},
			{"id":"pp","title":"Оба","auth":"token","token":"PPTOK","perms":["view.participants","view.parent_links"]}]}`)
	writeTestFile(t, filepath.Join(dataDir, "groups", "g2", "group.json"), `{"title":"Бета","update":false,"student_ids":["s1"]}`)
	writeTestFile(t, filepath.Join(dataDir, "groups", "g3", "group.json"), `{"title":"Объединение","student_ids":[],"member_groups":["g1","g2"]}`)
	writeTestFile(t, filepath.Join(dataDir, "groups", "g4", "group.json"), `{"title":"Чужая","student_ids":["s2"]}`)

	task := func(label string) string {
		return `{"label":"` + label + `","url":"https://acmp.ru/?main=task&id_task=1","normalized_url":"n-` + label + `","weight":42}`
	}
	contest := func(id, title, extra, s1Status string, s1Solved int, s2Status string) string {
		return `{"id":"` + id + `","title":"` + title + `","score_system":"edu"` + extra + `,
			"subcontests":[{"title":"Задачи","task_count":1,"tasks":[` + task("A") + `]}],
			"tasks":[` + task("A") + `],
			"rows":[
				{"student_id":"s2","public_name":"Петров П.","place":"1","statuses":["` + s2Status + `"],"solved_count":1},
				{"student_id":"s1","public_name":"Иванов И.","place":"2","statuses":["` + s1Status + `"],"solved_count":` + strconv.Itoa(s1Solved) + `}]}`
	}
	writeTestFile(t, filepath.Join(genDir, "standings", "g1.json"), `{
		"group_slug":"g1","group_title":"Альфа",
		"solved_summary_sites":["acmp"],
		"solved_summary":[
			{"student_id":"s2","public_name":"Петров П.","solved_count_on_page_sites":5,"total_solved_count":5,"solved_count_by_site":[5]},
			{"student_id":"s1","public_name":"Иванов И.","solved_count_on_page_sites":3,"total_solved_count":3,"solved_count_by_site":[3]}],
		"grades":{"title":"Оценки за модуль","columns":[{"title":"Контрольная","weight":1}],"decimals":1,
			"rows":[
				{"student_id":"s2","public_name":"Петров П.","values":[9.25],"final":9.3},
				{"student_id":"s1","public_name":"Иванов И.","values":[7.75],"final":7.8}]},
		"contests":[
			`+contest("c1", "Первый контест", "", "solved", 1, "solved")+`,
			`+contest("c2", "Не начатый", "", "none", 0, "solved")+`,
			`+contest("secret", "Скрытый контест", `,"hidden":true`, "solved", 1, "solved")+`,
			`+contest("shared", "Общий контест", "", "solved", 1, "solved")+`]}`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g2.json"), `{
		"group_slug":"g2","group_title":"Бета",
		"contests":[
			`+contest("shared", "Общий контест", "", "attempted", 0, "solved")+`,
			`+contest("c9", "Контест беты", "", "attempted", 0, "solved")+`]}`)
	writeTestFile(t, filepath.Join(genDir, "standings", "g4.json"), `{
		"group_slug":"g4","group_title":"Чужая",
		"contests":[`+contest("c4", "Контест чужой группы", "", "solved", 1, "solved")+`]}`)
	return h, dataDir
}

func childGet(t *testing.T, h *Handlers, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.StudentChildPage(rec, req)
	return rec
}

func TestStudentChildPageShowsOnlyPublicOwnResults(t *testing.T) {
	h, _ := childTestSetup(t)
	rec := childGet(t, h, childLink(t, "s1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()

	// Ключ в адресе — пропуск: не уходит в Referer, не индексируется, не кэшируется.
	for header, want := range map[string]string{
		"Referrer-Policy": "no-referrer",
		"X-Robots-Tag":    "noindex, nofollow",
		"Cache-Control":   "no-store",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	mustHave := map[string]string{
		"Иванов И.":      "имя ученика",
		"Альфа":          "его группа",
		"Бета":           "его архивная группа",
		"Первый контест": "контест, где он решал",
		"Контест беты":   "контест второй группы",
		"Решено задач по сайтам": "его строка доски почёта",
		"Оценки за модуль":       "его строка оценок",
		"7.8":                    "его оценка (1 знак после запятой, как на странице оценок)",
	}
	for s, what := range mustHave {
		if !strings.Contains(body, s) {
			t.Errorf("на странице должно быть: %s (%q)", what, s)
		}
	}
	mustNot := map[string]string{
		"Петров":               "чужое имя",
		"9.3":                  "чужая оценка",
		"Не начатый":           "контест без его посылок",
		"Скрытый контест":      "скрытый контест",
		"Объединение":          "объединённая группа",
		"Чужая":                "группа, где его нет",
		"Контест чужой группы": "контест чужой группы",
		"≈42′":                 "время решения задач (закрытая статистика)",
		`class="task-weight"`:  "время решения задач (закрытая статистика)",
		">Место<":              "колонка мест",
		"/student?id=":         "ссылка на закрытый профиль",
	}
	for s, what := range mustNot {
		if strings.Contains(body, s) {
			t.Errorf("на странице не должно быть: %s (%q)", what, s)
		}
	}
	// Общий контест — один раз, в первой группе (активная «Альфа»).
	if n := strings.Count(body, "<h2>Общий контест</h2>"); n != 1 {
		t.Errorf("общий контест должен быть показан один раз, показан %d", n)
	}
	if !strings.Contains(body, `id="contest-g1-shared"`) || strings.Contains(body, `id="contest-g2-shared"`) {
		t.Error("общий контест должен остаться в первой (активной) группе")
	}
}

func TestStudentChildPageRejectsBadKeys(t *testing.T) {
	h, _ := childTestSetup(t)
	for _, target := range []string{
		"/standings/child",
		"/standings/child?key=",
		"/standings/child?key=s1",
		"/standings/child?key=00000000000000000000000000000000",
		"/standings/child?key=zz112233445566778899aabbccddeeff",
		childLink(t, "s1") + "00",
	} {
		if code := childGet(t, h, target).Code; code != http.StatusNotFound {
			t.Errorf("%s: code=%d, want 404", target, code)
		}
	}
	// Ключ не зависит от регистра (вдруг родитель перепечатал ссылку).
	upper := "/standings/child?key=" + strings.ToUpper(strings.TrimPrefix(childLink(t, "s1"), "/standings/child?key="))
	if code := childGet(t, h, upper).Code; code != http.StatusOK {
		t.Errorf("ключ в верхнем регистре: code=%d", code)
	}
}

// Ключ подхватывается и меняется без перезапуска; без ключа ссылок нет.
func TestStudentChildLinksFollowSecretAndStudents(t *testing.T) {
	h, dataDir := childTestSetup(t)
	link := childLink(t, "s1")
	if code := childGet(t, h, link).Code; code != http.StatusOK {
		t.Fatalf("с ключом: code=%d", code)
	}

	// Новый ученик — ссылка работает сразу, без перезапуска.
	time.Sleep(10 * time.Millisecond)
	writeTestFile(t, filepath.Join(dataDir, "students.json"), `[
		{"id":"s1","full_name":"Иванов Иван","public_name":"Иванов И."},
		{"id":"s2","full_name":"Петров Пётр","public_name":"Петров П."},
		{"id":"s5","full_name":"Сидоров Сидор","public_name":"Сидоров С."}]`)
	if code := childGet(t, h, childLink(t, "s5")).Code; code != http.StatusOK {
		t.Errorf("ссылка нового ученика: code=%d", code)
	}
	if code := childGet(t, h, link).Code; code != http.StatusOK {
		t.Errorf("ссылка s1 должна остаться прежней, code=%d", code)
	}

	// Смена ключа — все старые ссылки перестают работать.
	writeTestFile(t, filepath.Join(dataDir, "credentials", "student_links_key.json"),
		`{"key":"ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"}`)
	if code := childGet(t, h, link).Code; code != http.StatusNotFound {
		t.Errorf("после смены ключа старая ссылка должна давать 404, code=%d", code)
	}

	// Нет файла ключа — ссылок нет вовсе.
	if err := os.Remove(filepath.Join(dataDir, "credentials", "student_links_key.json")); err != nil {
		t.Fatal(err)
	}
	if code := childGet(t, h, link).Code; code != http.StatusNotFound {
		t.Errorf("без ключа: code=%d", code)
	}
	if h.StudentLinksStatus() == nil {
		t.Error("без ключа статус должен сообщать, что ссылки выключены")
	}

	// Короткий ключ — негодный: такие ссылки реально перебрать.
	writeTestFile(t, filepath.Join(dataDir, "credentials", "student_links_key.json"), `{"key":"0011"}`)
	if h.StudentLinksStatus() == nil {
		t.Error("слишком короткий ключ должен отвергаться")
	}
}

func TestParentLinksPageFollowsPerm(t *testing.T) {
	get := func(t *testing.T, h *Handlers, handler http.HandlerFunc, target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("group_name", "g1")
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	t.Run("с правом — ссылки всех учеников группы", func(t *testing.T) {
		h, _ := childTestSetup(t)
		rec := get(t, h, h.GroupParentLinksPage, "/standings/g1/manage/parent-links?token=PLTOK")
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d", rec.Code)
		}
		body := rec.Body.String()
		for _, id := range []string{"s1", "s2"} {
			if !strings.Contains(body, childLink(t, id)) {
				t.Errorf("должна быть ссылка ученика %s", id)
			}
		}
		page := get(t, h, h.GroupStandingsPage, "/standings/g1?token=PLTOK").Body.String()
		if !strings.Contains(page, "/standings/g1/manage/parent-links?token=PLTOK") {
			t.Error("на странице группы должна быть кнопка «Ссылки для родителей»")
		}
	})

	t.Run("без права — нет ни страницы, ни кнопки", func(t *testing.T) {
		h, _ := childTestSetup(t)
		if code := get(t, h, h.GroupParentLinksPage, "/standings/g1/manage/parent-links?token=VPTOK").Code; code != http.StatusForbidden {
			t.Errorf("с другим правом: code=%d, want 403", code)
		}
		if code := get(t, h, h.GroupParentLinksPage, "/standings/g1/manage/parent-links").Code; code == http.StatusOK {
			t.Errorf("анониму страница со ссылками недоступна, code=%d", code)
		}
		for _, target := range []string{"/standings/g1?token=VPTOK", "/standings/g1"} {
			if strings.Contains(get(t, h, h.GroupStandingsPage, target).Body.String(), "parent-links") {
				t.Errorf("%s: кнопки «Ссылки для родителей» быть не должно", target)
			}
		}
	})

	t.Run("без ключа — объясняем, как завести", func(t *testing.T) {
		h, dataDir := childTestSetup(t)
		if err := os.Remove(filepath.Join(dataDir, "credentials", "student_links_key.json")); err != nil {
			t.Fatal(err)
		}
		body := get(t, h, h.GroupParentLinksPage, "/standings/g1/manage/parent-links?token=PLTOK").Body.String()
		if !strings.Contains(body, "Ссылки выключены") || strings.Contains(body, "/standings/child?key=") {
			t.Error("без ключа ссылок быть не должно, а подсказка — должна")
		}
	})

	t.Run("профиль ученика — ссылка только по праву", func(t *testing.T) {
		h, _ := childTestSetup(t)
		profile := func(token string) string {
			req := httptest.NewRequest(http.MethodGet, "/standings/g1/student?id=s1&token="+token, nil)
			req.SetPathValue("group_name", "g1")
			rec := httptest.NewRecorder()
			h.GroupStudentProfilePage(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("token=%s: code=%d", token, rec.Code)
			}
			return rec.Body.String()
		}
		if !strings.Contains(profile("PPTOK"), childLink(t, "s1")) {
			t.Error("с правом в профиле должна быть ссылка для родителей")
		}
		if strings.Contains(profile("VPTOK"), "/standings/child?key=") {
			t.Error("без права ссылки в профиле быть не должно")
		}
	})

	t.Run("админка", func(t *testing.T) {
		h, _ := childTestSetup(t)
		req := httptest.NewRequest(http.MethodGet, "/standings/admin/group-parent-links?slug=g1", nil)
		rec := httptest.NewRecorder()
		h.AdminGroupParentLinksPage(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), childLink(t, "s1")) {
			t.Errorf("админ должен видеть ссылки группы, code=%d", rec.Code)
		}
	})
}
