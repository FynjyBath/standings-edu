package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
	"standings-edu/internal/studentintake"
)

// groupStudentIDs — состав группы из data/groups/<slug>/group.json.
func groupStudentIDs(t *testing.T, h *Handlers, slug string) []string {
	t.Helper()
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		t.Fatalf("group %s not found: %v", slug, err)
	}
	return gf.StudentIDs
}

// Матрица прав состава: наблюдатель и жюри состав не трогают, админ группы —
// трогает. Проверяем и превью, и применение, и удаление.
func TestMembersPermMatrix(t *testing.T) {
	h, _ := juryTestSetup(t)

	names := map[string]any{"slug": "g1", "names": "Иванов Иван"}
	cases := []struct {
		name    string
		token   string
		handler http.HandlerFunc
		body    map[string]any
		want    int
	}{
		{"наблюдатель: список ФИО", tokObserver, h.PanelRegisterNamesDryRun, names, http.StatusForbidden},
		{"жюри: список ФИО", tokJury, h.PanelRegisterNamesDryRun, names, http.StatusForbidden},
		{"админ: список ФИО", tokAdmin, h.PanelRegisterNamesDryRun, names, http.StatusOK},
		{"наблюдатель: убрать", tokObserver, h.PanelMemberRemove, map[string]any{"slug": "g1", "student_id": "s1"}, http.StatusForbidden},
		{"жюри: убрать", tokJury, h.PanelMemberRemove, map[string]any{"slug": "g1", "student_id": "s1"}, http.StatusForbidden},
		{"админ: убрать", tokAdmin, h.PanelMemberRemove, map[string]any{"slug": "g1", "student_id": "s1"}, http.StatusOK},
		{"чужой токен", "WRONG", h.PanelRegisterNamesDryRun, names, http.StatusForbidden},
		{"без токена", "", h.PanelRegisterNamesDryRun, names, http.StatusForbidden},
	}
	for _, c := range cases {
		if code, _ := juryPost(t, c.handler, c.token, c.body); code != c.want {
			t.Errorf("%s: code=%d, ожидался %d", c.name, code, c.want)
		}
	}

	// Убрали — состав пуст, но сама запись ученика осталась в общей базе.
	if ids := groupStudentIDs(t, h, "g1"); len(ids) != 0 {
		t.Errorf("после удаления состав = %v, ожидался пустой", ids)
	}
	students, err := h.loadStudentsList()
	if err != nil || len(students) != 1 || students[0].ID != "s1" {
		t.Errorf("запись ученика не должна удаляться из students.json: %v (%v)", students, err)
	}
}

// Добавление существующего ученика по ФИО работает без права заводить новых, а
// незнакомое ФИО в этом режиме не создаёт запись в общей базе.
func TestMembersAddExistingWithoutRegisterPerm(t *testing.T) {
	h, _ := juryTestSetup(t)

	// Доступ только с правом распоряжаться составом (без members.register).
	gf, ok, err := h.readGroupFile("g1")
	if err != nil || !ok {
		t.Fatal("group not found")
	}
	gf.StudentIDs = nil // очистим состав, чтобы добавление было видно
	gf.Accesses = append(gf.Accesses, domain.AccessEntry{
		ID: "mem", Title: "Состав", Auth: domain.AccessAuthToken, Token: "mtok",
		Perms: []domain.Perm{domain.PermMembersManage},
	})
	if err := h.writeGroupFile("g1", gf); err != nil {
		t.Fatal(err)
	}

	// Известное ФИО — добавляется в группу.
	code, resp := juryPost(t, h.PanelRegisterNamesApply, "mtok", map[string]any{
		"slug": "g1", "names": "Иванов Иван",
	})
	if code != http.StatusOK {
		t.Fatalf("добавление существующего: code=%d, resp=%v", code, resp)
	}
	ids := groupStudentIDs(t, h, "g1")
	if len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("состав после добавления = %v, ожидался [s1]", ids)
	}

	// Незнакомое ФИО — пропуск: ни ученика, ни участника.
	code, resp = juryPost(t, h.PanelRegisterNamesApply, "mtok", map[string]any{
		"slug": "g1", "names": "Совершенно Новый Человек",
	})
	if code != http.StatusOK {
		t.Fatalf("незнакомое ФИО: code=%d, resp=%v", code, resp)
	}
	plan, _ := resp["plan"].(map[string]any)
	if plan == nil || plan["create"].(float64) != 0 {
		t.Fatalf("без права заводить учеников create должен быть 0: %v", plan)
	}
	students, err := h.loadStudentsList()
	if err != nil {
		t.Fatal(err)
	}
	if len(students) != 1 {
		t.Fatalf("students.json не должен пополняться: %v", students)
	}
	if ids := groupStudentIDs(t, h, "g1"); len(ids) != 1 {
		t.Fatalf("состав не должен меняться: %v", ids)
	}

	// А с правом members.register тот же список заводит ученика.
	code, _ = juryPost(t, h.PanelRegisterNamesApply, tokAdmin, map[string]any{
		"slug": "g1", "names": "Совершенно Новый Человек",
	})
	if code != http.StatusOK {
		t.Fatalf("с правом register: code=%d", code)
	}
	students, _ = h.loadStudentsList()
	if len(students) != 2 {
		t.Fatalf("ожидалось заведение нового ученика, стало: %v", students)
	}
}

// Правка данных ученика: только своих и только существующих.
func TestStudentEditScope(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	// Ученик другой группы — в состав g1 не входит.
	if err := os.WriteFile(filepath.Join(dataDir, "students.json"),
		[]byte(`[{"id":"s1","full_name":"Иванов Иван","public_name":"Иванов И."},
		         {"id":"s2","full_name":"Петров Пётр","public_name":"Петров П."}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Свой ученик — правится.
	code, resp := juryPost(t, h.PanelStudentSave, tokAdmin, map[string]any{
		"slug": "g1", "id": "s1", "full_name": "Иванов Иван", "public_name": "Иванов И. И.",
		"accounts": []map[string]string{{"site": "codeforces", "account_id": "ivanov"}},
	})
	if code != http.StatusOK {
		t.Fatalf("правка своего ученика: code=%d, resp=%v", code, resp)
	}
	students, _ := h.loadStudentsList()
	if students[0].PublicName != "Иванов И. И." || len(students[0].Accounts) != 1 {
		t.Fatalf("данные не сохранились: %+v", students[0])
	}

	// Чужой ученик — 403.
	if code, _ := juryPost(t, h.PanelStudentSave, tokAdmin, map[string]any{
		"slug": "g1", "id": "s2", "full_name": "Петров Пётр", "public_name": "Взлом",
	}); code != http.StatusForbidden {
		t.Errorf("чужой ученик: code=%d, ожидался 403", code)
	}

	// Новый ученик (без id) — заводить из панели нельзя.
	if code, _ := juryPost(t, h.PanelStudentSave, tokAdmin, map[string]any{
		"slug": "g1", "full_name": "Ещё Один Ученик",
	}); code != http.StatusForbidden {
		t.Errorf("создание ученика из панели: code=%d, ожидался 403", code)
	}

	// Жюри данные учеников не правит.
	if code, _ := juryPost(t, h.PanelStudentSave, tokJury, map[string]any{
		"slug": "g1", "id": "s1", "full_name": "Иванов Иван",
	}); code != http.StatusForbidden {
		t.Errorf("жюри: code=%d, ожидался 403", code)
	}
}

// Приём анкеты группы: ученик заводится и попадает в состав, анкета уходит из
// очереди. Анкета, поданная в две группы, остаётся ждать вторую.
func TestIntakeMergeGroupScope(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	// Приём анкет тестовый харнесс не настраивает — заводим стор вручную.
	intakePath := filepath.Join(dataDir, "student_intake.json")
	h.intake = studentintake.NewStore(intakePath)

	if err := os.WriteFile(intakePath, []byte(`[
		{"full_name":"Новиков Новик","groups":["g1"],"accounts":[{"site":"codeforces","account_id":"nov"}]},
		{"full_name":"Двойной Ученик","groups":["g1","g2"]},
		{"full_name":"Чужой Ученик","groups":["g2"]}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Жюри принять не может — только смотреть.
	if code, _ := juryPost(t, h.PanelIntakeAccept, tokJury, map[string]any{"slug": "g1"}); code != http.StatusForbidden {
		t.Errorf("жюри: code=%d, ожидался 403", code)
	}

	code, resp := juryPost(t, h.PanelIntakeAccept, tokAdmin, map[string]any{"slug": "g1"})
	if code != http.StatusOK {
		t.Fatalf("приём анкет: code=%d, resp=%v", code, resp)
	}
	if resp["accepted"].(float64) != 2 {
		t.Fatalf("принято %v, ожидалось 2", resp["accepted"])
	}

	// Оба ученика теперь в составе g1.
	ids := groupStudentIDs(t, h, "g1")
	if len(ids) != 3 { // s1 + двое принятых
		t.Fatalf("состав g1 = %v, ожидалось 3 участника", ids)
	}
	// Аккаунт из анкеты доехал.
	students, _ := h.loadStudentsList()
	found := false
	for _, s := range students {
		if s.FullName == "Новиков Новик" && len(s.Accounts) == 1 && s.Accounts[0].AccountID == "nov" {
			found = true
		}
	}
	if !found {
		t.Fatalf("аккаунт из анкеты не сохранён: %+v", students)
	}

	// В очереди осталась чужая анкета и «двойная» — но уже только для g2.
	pending, err := h.intake.IntakeQueue(filepath.Join(dataDir, "student_intake_admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("в очереди %d анкет, ожидалось 2: %+v", len(pending), pending)
	}
	for _, p := range pending {
		for _, g := range p.Groups {
			if g == "g1" {
				t.Fatalf("анкета %q осталась висеть на g1: %+v", p.FullName, p)
			}
		}
	}
}

// Действия по группе требуют своих прав; жюри пересобирает, но кеш не сбрасывает.
func TestActionsPermMatrix(t *testing.T) {
	h, _ := juryTestSetup(t)

	// Действий нет ни в одном пресете: даже админу группы они закрыты, пока
	// право не отмечено галочкой.
	for _, token := range []string{tokObserver, tokJury, tokAdmin} {
		for name, handler := range map[string]http.HandlerFunc{
			"generate":    h.PanelActionGenerate,
			"reset-cache": h.PanelActionResetCache,
		} {
			if code, _ := juryPost(t, handler, token, map[string]any{"slug": "g1"}); code != http.StatusForbidden {
				t.Errorf("%s по пресетному доступу: code=%d, ожидался 403", name, code)
			}
		}
	}

	// Доступ с явно отмеченными действиями — работает (и только в своей группе).
	gf, ok, err := h.readGroupFile("g1")
	if err != nil || !ok {
		t.Fatal("group not found")
	}
	gf.Accesses = append(gf.Accesses, domain.AccessEntry{
		ID: "act", Title: "Действия", Auth: domain.AccessAuthToken, Token: "acttok",
		Perms: []domain.Perm{domain.PermActionsGenerate, domain.PermActionsResetCache},
	})
	if err := h.writeGroupFile("g1", gf); err != nil {
		t.Fatal(err)
	}
	if code, _ := juryPost(t, h.PanelActionResetCache, "acttok", map[string]any{"slug": "g1"}); code != http.StatusOK {
		t.Errorf("доступ с правом reset-cache: code=%d, ожидался 200", code)
	}
	// Действие по чужой группе — 403 (токен g1 в g2 не действует).
	if code, _ := juryPost(t, h.PanelActionGenerate, "acttok", map[string]any{"slug": "g2"}); code != http.StatusForbidden {
		t.Errorf("generate по чужой группе: code=%d, ожидался 403", code)
	}
}

// Пресеты: «Админ группы» распоряжается составом и действиями, «Жюри» — только
// пересборкой, «Наблюдатель» — ничем из этого.
func TestPresetsCoverNewPerms(t *testing.T) {
	observer := domain.NewPermSet(domain.ObserverPerms()...)
	jury := domain.NewPermSet(domain.JuryPerms()...)
	admin := domain.NewPermSet(domain.AdminPerms()...)

	checks := []struct {
		name string
		set  domain.PermSet
		perm domain.Perm
		want bool
	}{
		{"наблюдатель/состав", observer, domain.PermMembersManage, false},
		{"наблюдатель/анкеты", observer, domain.PermIntakeView, true},
		{"жюри/состав", jury, domain.PermMembersManage, false},
		{"админ/состав", admin, domain.PermMembersManage, true},
		{"админ/регистрация", admin, domain.PermMembersRegister, true},
		{"админ/ученики", admin, domain.PermStudentsEdit, true},
		{"админ/приём анкет", admin, domain.PermIntakeMerge, true},
		// Действия нагружают сеть и источники — ни в одном пресете их нет,
		// только галочкой.
		{"наблюдатель/генерация", observer, domain.PermActionsGenerate, false},
		{"жюри/генерация", jury, domain.PermActionsGenerate, false},
		{"админ/генерация", admin, domain.PermActionsGenerate, false},
		{"админ/кеш", admin, domain.PermActionsResetCache, false},
	}
	for _, c := range checks {
		if got := c.set.Has(c.perm); got != c.want {
			t.Errorf("%s: %s = %v, ожидалось %v", c.name, c.perm, got, c.want)
		}
	}

	// Каждое новое право должно быть в каталоге — иначе Validate его отвергнет
	// и доступ с ним нельзя будет сохранить.
	for _, p := range []domain.Perm{
		domain.PermMembersManage, domain.PermMembersRegister, domain.PermStudentsEdit,
		domain.PermIntakeMerge, domain.PermActionsGenerate, domain.PermActionsResetCache,
	} {
		if !domain.KnownPerm(p) {
			t.Errorf("право %s отсутствует в каталоге", p)
		}
	}
}

// Отсечка объединённых групп: своего состава у них нет.
func TestMembersRejectCombinedGroup(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	combined := testGroupJSON(t, map[string]any{
		"title":         "Объединённая",
		"member_groups": []string{"g1"},
		"accesses":      testAccesses(),
	})
	path := filepath.Join(dataDir, "groups", "gc", "group.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(combined), 0o644); err != nil {
		t.Fatal(err)
	}

	code, resp := juryPost(t, h.PanelRegisterNamesApply, tokAdmin, map[string]any{
		"slug": "gc", "names": "Иванов Иван",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("объединённая группа: code=%d, ожидался 400 (resp=%v)", code, resp)
	}
	blob, _ := json.Marshal(resp)
	if !strings.Contains(string(blob), "объединённой") {
		t.Errorf("ожидалось понятное сообщение, получено: %s", blob)
	}
}

// Страницы панели должны не только парситься, но и отрисовываться: шаблон
// лезет в поля данных и в $.CanMerge/$.CanEditStudents внутри range.
func TestPanelPagesRender(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	h.intake = studentintake.NewStore(filepath.Join(dataDir, "student_intake.json"))
	if err := os.WriteFile(filepath.Join(dataDir, "student_intake.json"),
		[]byte(`[{"full_name":"Новиков Новик","groups":["g1"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	pages := []struct {
		name    string
		handler http.HandlerFunc
		token   string
		want    []string
	}{
		{"состав/админ", h.GroupManageMembersPage, tokAdmin, []string{"Состав группы", "Иванов И.", "Править"}},
		{"анкеты/админ", h.GroupIntakePage, tokAdmin, []string{"Новиков Новик", `id="intake-accept"`, `id="intake-editor"`}},
		{"анкеты/жюри", h.GroupIntakePage, tokJury, []string{"Новиков Новик"}},
	}
	for _, p := range pages {
		rec := accessGet(t, p.handler, "/x?token="+p.token, "g1")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: code=%d, тело=%s", p.name, rec.Code, rec.Body.String())
			continue
		}
		for _, want := range p.want {
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("%s: в странице нет %q", p.name, want)
			}
		}
	}

	// Жюри анкеты видит, но управления приёмом у него нет (скрипт страницы
	// упоминает те же id, поэтому ищем именно разметку кнопок).
	rec := accessGet(t, h.GroupIntakePage, "/x?token="+tokJury, "g1")
	for _, marker := range []string{`id="intake-accept"`, `class="intake-pick"`, `id="intake-editor"`} {
		if strings.Contains(rec.Body.String(), marker) {
			t.Errorf("жюри не должно видеть управление приёмом (%s)", marker)
		}
	}
	// Наблюдателю состав закрыт.
	if rec := accessGet(t, h.GroupManageMembersPage, "/x?token="+tokObserver, "g1"); rec.Code != http.StatusForbidden {
		t.Errorf("наблюдатель на странице состава: code=%d, ожидался 403", rec.Code)
	}
}

// Глобальный доступ со scope=all покрывает любой слаг — но операции состава не
// должны создавать группу «по дороге» (её заводит только админка).
func TestPanelOperationsRejectUnknownGroup(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	h.intake = studentintake.NewStore(filepath.Join(dataDir, "student_intake.json"))
	if err := os.WriteFile(filepath.Join(dataDir, "student_intake.json"),
		[]byte(`[{"full_name":"Новиков Новик","groups":["ghost"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Глобальный доступ на все группы с полным набором прав.
	writeTestFile(t, filepath.Join(dataDir, "credentials", "global_accesses.json"),
		`[{"id":"glob","title":"Куратор","auth":"token","token":"gtok","scope":"all",
		   "perms":["members.manage","members.register","students.edit","intake.merge","actions.generate"]}]`)

	cases := map[string]http.HandlerFunc{
		"intake":   h.PanelIntakeAccept,
		"register": h.PanelRegisterNamesApply,
		"remove":   h.PanelMemberRemove,
		"generate": h.PanelActionGenerate,
	}
	for name, handler := range cases {
		code, resp := juryPost(t, handler, "gtok", map[string]any{
			"slug": "ghost", "names": "Иванов Иван", "student_id": "s1",
		})
		if code == http.StatusOK {
			t.Errorf("%s по несуществующей группе прошёл: resp=%v", name, resp)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "groups", "ghost")); !os.IsNotExist(err) {
		t.Error("группа ghost не должна была появиться на диске")
	}
}

// Панельные ручки анкет обязаны требовать валидный слаг группы. Пустой slug
// когда-то давал пустую область — неотличимую от админской — и запрос без
// всякой авторизации принимал разом все анкеты сайта.
func TestPanelIntakeRequiresGroup(t *testing.T) {
	h, dataDir := juryTestSetup(t)
	h.intake = studentintake.NewStore(filepath.Join(dataDir, "student_intake.json"))
	if err := os.WriteFile(filepath.Join(dataDir, "student_intake.json"),
		[]byte(`[{"full_name":"Секретная Анкета","groups":["g1"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dataDir, "students.json"))
	if err != nil {
		t.Fatal(err)
	}

	handlers := map[string]http.HandlerFunc{
		"accept":        h.PanelIntakeAccept,
		"preview":       h.PanelIntakePreview,
		"entry/save":    h.PanelIntakeEntrySave,
		"entry/discard": h.PanelIntakeEntryDiscard,
	}
	bodies := map[string]map[string]any{
		"без slug":     {},
		"пустой slug":  {"slug": ""},
		"одни пробелы": {"slug": "   "},
		"обход пути":   {"slug": "../../etc"},
		"нет прав":     {"slug": "g1"},
	}
	for name, handler := range handlers {
		for what, body := range bodies {
			// Без токена вовсе: ни одна ручка не должна отработать.
			code, resp := juryPost(t, handler, "", body)
			if code == http.StatusOK {
				t.Errorf("%s (%s): запрос без прав прошёл, resp=%v", name, what, resp)
			}
		}
	}

	// Очередь и база не тронуты.
	queue, err := h.intake.IntakeQueue(filepath.Join(dataDir, "student_intake_admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 {
		t.Fatalf("очередь изменилась: %+v", queue)
	}
	after, _ := os.ReadFile(filepath.Join(dataDir, "students.json"))
	if string(before) != string(after) {
		t.Fatalf("students.json изменился:\nбыло: %s\nстало: %s", before, after)
	}
}
