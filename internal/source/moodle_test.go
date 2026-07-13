package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"standings-edu/internal/domain"
)

func TestParseMoodleActivityURL(t *testing.T) {
	ok := map[string]int{
		"https://kurs.kod-u.ru/mod/quiz/view.php?id=5":        5,
		"https://kurs.kod-u.ru/mod/assign/view.php?id=123":    123,
		"https://kurs.kod-u.ru/mod/lesson/view.php?id=7&x=1 ": 7,
	}
	for raw, want := range ok {
		got, err := parseMoodleActivityURL(raw)
		if err != nil || got != want {
			t.Errorf("%q -> (%d,%v), want %d", raw, got, err, want)
		}
	}
	bad := []string{
		"https://kurs.kod-u.ru/course/view.php?id=3",
		"https://kurs.kod-u.ru/mod/quiz/view.php",
		"https://kurs.kod-u.ru/mod/quiz/view.php?id=abc",
	}
	for _, raw := range bad {
		if _, err := parseMoodleActivityURL(raw); err == nil {
			t.Errorf("%q must fail", raw)
		}
	}
}

// Матчинг «Имя Фамилия» (Moodle) против «Фамилия Имя Отчество» (standings):
// пословно, без учёта порядка; тёзки с одинаковым качеством — не сопоставляются.
func TestMatchMoodleUsers(t *testing.T) {
	students := []domain.Student{
		{ID: "airiyan", FullName: "Айриян Яна Суреновна", PublicName: "Айриян Я."},
		{ID: "alimov", FullName: "Алимов Самат", PublicName: "Алимов С."},
		{ID: "ivanov-1", FullName: "Иванов Иван Петрович"},
		{ID: "ivanov-2", FullName: "Иванов Иван Сергеевич"},
		{ID: "only-public", PublicName: "Петров В."},
	}
	grades := []moodleUserGrade{
		{FullName: "Яна Айриян"},     // 0: обратный порядок
		{FullName: "Самат Алимов"},   // 1
		{FullName: "Иван Иванов"},    // 2: два тёзки → пропуск
		{FullName: "Василий Петров"}, // 3: по public name с инициалом
		{FullName: "Кто-то Другой"},  // 4: не матчится
	}
	m := matchMoodleUsers(grades, students)
	if m[0] != "airiyan" || m[1] != "alimov" {
		t.Fatalf("basic matching failed: %v", m)
	}
	if _, ok := m[2]; ok {
		t.Fatalf("namesakes must stay unmatched: %v", m)
	}
	if m[3] != "only-public" {
		t.Fatalf("initial match failed: %v", m)
	}
	if _, ok := m[4]; ok {
		t.Fatalf("stranger must stay unmatched: %v", m)
	}
}

// Один инициал не должен давать совпадение (нужно полное слово-фамилия).
func TestMoodleNameMatchScoreGuards(t *testing.T) {
	if _, ok := moodleNameMatchScore([]string{"я.", "с."}, []string{"яна", "суреновна"}); ok {
		t.Fatal("initials-only match must be rejected")
	}
	if _, ok := moodleNameMatchScore([]string{"яна"}, []string{"яна", "айриян"}); ok {
		t.Fatal("single-token name must be rejected")
	}
}

// Регрессия реального кейса: у ученика «Конопелько Артём» (public «Конопелько А.»)
// чужое имя «Артём Андреевичев» совпадало через инициал «а.» и перебивало
// настоящего «Артём Конопелько». Ученик с полным ФИО требует два точных слова,
// инициал даёт лишь 1 очко.
func TestMatchMoodleUsersInitialTrap(t *testing.T) {
	students := []domain.Student{
		{ID: "konopelko-a", FullName: "Конопелько Артём", PublicName: "Конопелько А."},
	}
	grades := []moodleUserGrade{
		{FullName: "Артём Андреевичев"}, // «артем» точно + «андреевичев»↔«а.» — должен быть отвергнут
		{FullName: "Артём Конопелько"},  // настоящий
	}
	m := matchMoodleUsers(grades, students)
	if len(m) != 1 || m[1] != "konopelko-a" {
		t.Fatalf("must match only the real user: %v", m)
	}
	// И даже если настоящего нет в курсе — чужой не подцепляется.
	m = matchMoodleUsers(grades[:1], students)
	if len(m) != 0 {
		t.Fatalf("stranger must not match via initial: %v", m)
	}
}

func TestFormatMoodleGrade(t *testing.T) {
	cases := map[float64]string{10: "10", 7.5: "7,5", 8.25: "8,25", 0: "0"}
	for v, want := range cases {
		if got := formatMoodleGrade(v); got != want {
			t.Errorf("formatMoodleGrade(%v)=%q want %q", v, got, want)
		}
	}
}

// Полный прогон провайдера против фейкового Moodle: display=percent, show_all.
func TestMoodleGradesProviderBuildStandings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/webservice/rest/server.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("wstoken") != "tok" {
			_ = json.NewEncoder(w).Encode(map[string]string{"exception": "x", "errorcode": "invalidtoken", "message": "bad token"})
			return
		}
		switch r.URL.Query().Get("wsfunction") {
		case "core_course_get_course_module":
			if r.URL.Query().Get("cmid") != "5" {
				t.Errorf("cmid=%q", r.URL.Query().Get("cmid"))
			}
			_, _ = w.Write([]byte(`{"cm":{"id":5,"course":3,"name":"Тест 1","modname":"quiz","instance":1}}`))
		case "gradereport_user_get_grade_items":
			if r.URL.Query().Get("courseid") != "3" {
				t.Errorf("courseid=%q", r.URL.Query().Get("courseid"))
			}
			_, _ = w.Write([]byte(`{"usergrades":[
				{"userid":10,"userfullname":"Яна Айриян","gradeitems":[
					{"cmid":5,"itemtype":"mod","graderaw":10,"grademax":10},
					{"itemtype":"course","graderaw":55,"grademax":100}]},
				{"userid":11,"userfullname":"Самат Алимов","gradeitems":[
					{"cmid":5,"itemtype":"mod","graderaw":7.5,"grademax":10}]},
				{"userid":12,"userfullname":"Посторонний Человек","gradeitems":[
					{"cmid":5,"itemtype":"mod","graderaw":4,"grademax":10}]},
				{"userid":13,"userfullname":"Без Оценки","gradeitems":[
					{"cmid":5,"itemtype":"mod","graderaw":null,"grademax":10}]}
			]}`))
		default:
			t.Errorf("unexpected wsfunction %q", r.URL.Query().Get("wsfunction"))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewMoodleClient(MoodleCredentials{BaseURL: srv.URL, Token: "tok"})
	provider := NewMoodleGradesProvider(client)

	cfgJSON, _ := json.Marshal(map[string]any{
		"activity_url": srv.URL + "/mod/quiz/view.php?id=5",
		"show_all":     true,
	})
	contest := domain.Contest{
		ID:             "moodle-test-1",
		Title:          "Тест 1 (Moodle)",
		ScoreSystem:    domain.ScoreSystemIOI,
		ContestType:    domain.ContestTypeProvider,
		Provider:       MoodleGradesProviderID,
		ProviderConfig: cfgJSON,
	}
	students := []domain.Student{
		{ID: "airiyan", FullName: "Айриян Яна Суреновна", PublicName: "Айриян Я."},
		{ID: "alimov", FullName: "Алимов Самат", PublicName: "Алимов С."},
		{ID: "no-grade", FullName: "Нигдеев Никто", PublicName: "Нигдеев Н."},
	}

	out, err := provider.BuildStandings(context.Background(), ContestProviderInput{
		Contest:  contest,
		Students: students,
	})
	if err != nil {
		t.Fatalf("BuildStandings: %v", err)
	}

	if len(out.Tasks) != 1 || out.Subcontests[0].Title != "Тест 1 (из 10)" {
		t.Fatalf("tasks/subcontest wrong: %+v", out.Subcontests)
	}
	// 3 ученика группы + 2 extra (посторонний и «Без Оценки»).
	if len(out.Rows) != 5 {
		t.Fatalf("rows=%d want 5: %+v", len(out.Rows), out.Rows)
	}

	byID := map[string]domain.GeneratedRow{}
	byName := map[string]domain.GeneratedRow{}
	for _, r := range out.Rows {
		byID[r.StudentID] = r
		byName[r.PublicName] = r
	}
	// Яна: 10/10 → percent 100, solved, «10 из 10».
	if r := byID["airiyan"]; r.TotalScore != 100 || r.Statuses[0] != domain.TaskStatusSolved || r.ProviderStatus != "10 из 10" {
		t.Fatalf("airiyan row wrong: %+v", r)
	}
	// Самат: 7.5/10 → percent 75, attempted, «7,5 из 10».
	if r := byID["alimov"]; r.TotalScore != 75 || r.Statuses[0] != domain.TaskStatusAttempted || r.ProviderStatus != "7,5 из 10" {
		t.Fatalf("alimov row wrong: %+v", r)
	}
	// Ученик без оценки в Moodle: пустая строка.
	if r := byID["no-grade"]; r.Statuses[0] != domain.TaskStatusNone || r.ProviderStatus != "" {
		t.Fatalf("no-grade row wrong: %+v", r)
	}
	// Посторонний (show_all): отдельная строка с процентом 40.
	if r := byName["Посторонний Человек"]; r.TotalScore != 40 || r.ProviderStatus != "4 из 10" {
		t.Fatalf("extra row wrong: %+v", r)
	}
	// Сортировка: сначала оценённые по убыванию (100, 75, 40, 0-graded), потом без оценки.
	if out.Rows[0].StudentID != "airiyan" || out.Rows[1].StudentID != "alimov" {
		t.Fatalf("sort wrong: %v %v", out.Rows[0].StudentID, out.Rows[1].StudentID)
	}
}

// Кеш на время генерации: два контеста из одного курса делают один запрос
// журнала оценок; повторный cmid — один запрос модуля курса.
func TestMoodleGradesProviderCachesReport(t *testing.T) {
	reportCalls, cmCalls := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/webservice/rest/server.php", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("wsfunction") {
		case "core_course_get_course_module":
			cmCalls++
			cmid := r.URL.Query().Get("cmid")
			_, _ = w.Write([]byte(`{"cm":{"id":` + cmid + `,"course":3,"name":"Тест ` + cmid + `","modname":"quiz","instance":1}}`))
		case "gradereport_user_get_grade_items":
			reportCalls++
			_, _ = w.Write([]byte(`{"usergrades":[
				{"userid":10,"userfullname":"Яна Айриян","gradeitems":[
					{"cmid":5,"itemtype":"mod","graderaw":10,"grademax":10},
					{"cmid":8,"itemtype":"mod","graderaw":6,"grademax":10}]}
			]}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	provider := NewMoodleGradesProvider(NewMoodleClient(MoodleCredentials{BaseURL: srv.URL, Token: "tok"}))
	students := []domain.Student{{ID: "airiyan", FullName: "Айриян Яна", PublicName: "Айриян Я."}}
	build := func(cmid string) {
		cfgJSON, _ := json.Marshal(map[string]any{"activity_url": srv.URL + "/mod/quiz/view.php?id=" + cmid})
		_, err := provider.BuildStandings(context.Background(), ContestProviderInput{
			Contest:  domain.Contest{ID: "c" + cmid, ScoreSystem: domain.ScoreSystemIOI, Provider: MoodleGradesProviderID, ProviderConfig: cfgJSON},
			Students: students,
		})
		if err != nil {
			t.Fatalf("build cmid=%s: %v", cmid, err)
		}
	}
	build("5")
	build("8")
	build("5") // повторный cmid
	if reportCalls != 1 {
		t.Fatalf("grade report must be fetched once, got %d", reportCalls)
	}
	if cmCalls != 2 {
		t.Fatalf("course module must be fetched once per cmid, got %d", cmCalls)
	}
}

// Перелогин: на invalidtoken при наличии логина/пароля токен добывается заново.
func TestMoodleClientRelogin(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/login/token.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "u" || r.URL.Query().Get("password") != "p" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad", "errorcode": "invalidlogin"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "fresh"})
	})
	mux.HandleFunc("/webservice/rest/server.php", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("wstoken") != "fresh" {
			_ = json.NewEncoder(w).Encode(map[string]string{"exception": "x", "errorcode": "invalidtoken", "message": "bad"})
			return
		}
		_, _ = w.Write([]byte(`{"cm":{"id":1,"course":2,"name":"n","modname":"quiz","instance":1}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Протухший статический токен + логин/пароль → перелогин и успех.
	client := NewMoodleClient(MoodleCredentials{BaseURL: srv.URL, Token: "stale", Username: "u", Password: "p", Service: "svc"})
	var cm moodleCourseModule
	if err := client.call(context.Background(), "core_course_get_course_module", url.Values{"cmid": {"1"}}, &cm); err != nil {
		t.Fatalf("call with relogin: %v", err)
	}
	if cm.CM.Course != 2 || calls != 2 {
		t.Fatalf("relogin flow wrong: course=%d calls=%d", cm.CM.Course, calls)
	}
}
