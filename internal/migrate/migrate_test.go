package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/storage"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readStrings(t *testing.T, path, key string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	json.Unmarshal(m[key], &out)
	return out
}

func contestIDs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, r := range raw {
		out = append(out, rawStringField(r, "id"))
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// sel строит выбор: перечисленные слаги — с участниками/контестами.
func sel(participants, contests []string) Selection {
	s := Selection{Participants: map[string]bool{}, Contests: map[string]bool{}}
	for _, g := range participants {
		s.Participants[g] = true
	}
	for _, g := range contests {
		s.Contests[g] = true
	}
	return s
}

func selBoth(slugs ...string) Selection { return sel(slugs, slugs) }

// source-директория: обычная группа grp + объединённая combo(из grp), ученики,
// глобальные контесты; grp ссылается на c1.
func makeSource(t *testing.T) string {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "students.json"), `[
	 {"id":"ivanov","full_name":"Иванов","public_name":"Иванов","accounts":[{"site":"codeforces","account_id":"tourist"}]},
	 {"id":"petrov","full_name":"Петров","public_name":"Петров","accounts":[{"site":"informatics","account_id":"555"}]},
	 {"id":"sidorov","full_name":"Сидоров","public_name":"Сидоров","accounts":[]}
	]`)
	write(t, filepath.Join(dir, "contests.json"), `[
	 {"id":"c1","title":"Контест 1","score_system":"edu"},
	 {"id":"c2","title":"Контест 2","score_system":"ioi"},
	 {"id":"c3","title":"Не используется","score_system":"edu"}
	]`)
	write(t, filepath.Join(dir, "groups", "grp", "group.json"),
		`{"title":"Группа","student_ids":["ivanov","petrov"],"group_secret_token":"secret123"}`)
	write(t, filepath.Join(dir, "groups", "grp", "contests.json"),
		`[{"id":"c1","update":true}]`)
	write(t, filepath.Join(dir, "groups", "combo", "group.json"),
		`{"title":"Объединение","member_groups":["grp"]}`)
	return dir
}

func studentIDs(bundle *Bundle) map[string]bool {
	out := map[string]bool{}
	for _, s := range bundle.Students {
		out[s.ID] = true
	}
	return out
}

func TestRoundTripNewTarget(t *testing.T) {
	src := makeSource(t)
	// Экспортируем только combo — grp должна подтянуться как участница целиком.
	bundle, err := BuildBundle(src, selBoth("combo"), true)
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, g := range bundle.Groups {
		slugs[g.Slug] = true
	}
	if !slugs["combo"] || !slugs["grp"] {
		t.Fatalf("combo и его участница grp должны быть в бандле, got %v", slugs)
	}
	got := studentIDs(bundle)
	if !got["ivanov"] || !got["petrov"] || got["sidorov"] {
		t.Fatalf("ученики бандла неверны: %v", got)
	}
	for _, s := range bundle.Students {
		if s.ID == "ivanov" && (len(s.Groups) != 1 || s.Groups[0] != "grp") {
			t.Fatalf("ivanov.Groups должно быть [grp], got %v", s.Groups)
		}
	}
	gotC := map[string]bool{}
	for _, c := range bundle.Contests {
		gotC[rawStringField(c, "id")] = true
	}
	if !gotC["c1"] || gotC["c2"] || gotC["c3"] {
		t.Fatalf("контесты бандла неверны: %v", gotC)
	}

	dst := t.TempDir()
	rep, err := ImportBundle(dst, bundle, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.StudentsAdded != 2 || rep.ContestsAdded != 1 {
		t.Fatalf("отчёт: students=%d contests=%d", rep.StudentsAdded, rep.ContestsAdded)
	}
	if ids := contestIDs(t, filepath.Join(dst, "groups", "grp", "contests.json")); len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("grp contests: %v", ids)
	}
	if sids := readStrings(t, filepath.Join(dst, "groups", "grp", "group.json"), "student_ids"); len(sids) != 2 {
		t.Fatalf("grp student_ids: %v", sids)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "groups", "grp", "group.json"))
	if !strings.Contains(string(b), "secret123") {
		t.Fatal("токен должен перенестись при includeTokens=true")
	}
}

func TestExportParticipantsOnly(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, sel([]string{"grp"}, nil), true)
	if err != nil {
		t.Fatal(err)
	}
	got := studentIDs(bundle)
	if !got["ivanov"] || !got["petrov"] {
		t.Fatalf("участники должны быть в бандле: %v", got)
	}
	if len(bundle.Contests) != 0 {
		t.Fatalf("контестов быть не должно: %v", bundle.Contests)
	}
	for _, g := range bundle.Groups {
		if g.Slug == "grp" && len(g.Contests) != 0 {
			t.Fatalf("у grp контесты не должны выгружаться: %v", g.Contests)
		}
	}
}

func TestExportContestsOnly(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, sel(nil, []string{"grp"}), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Students) != 0 {
		t.Fatalf("учеников быть не должно (участники не выбраны): %v", bundle.Students)
	}
	gotC := map[string]bool{}
	for _, c := range bundle.Contests {
		gotC[rawStringField(c, "id")] = true
	}
	if !gotC["c1"] {
		t.Fatalf("c1 должен быть выгружен: %v", gotC)
	}
	// student_ids вырезаны из group.json.
	for _, g := range bundle.Groups {
		if g.Slug == "grp" && strings.Contains(string(g.Group), "student_ids") {
			t.Fatal("student_ids должны быть вырезаны при экспорте только контестов")
		}
	}
}

func TestExportCombinedPullsMembersFully(t *testing.T) {
	src := makeSource(t)
	// combo только по контестам (своих нет) — участница grp должна прийти целиком.
	bundle, err := BuildBundle(src, sel(nil, []string{"combo"}), true)
	if err != nil {
		t.Fatal(err)
	}
	got := studentIDs(bundle)
	if !got["ivanov"] || !got["petrov"] {
		t.Fatalf("участница grp должна прийти с участниками: %v", got)
	}
	gotC := map[string]bool{}
	for _, c := range bundle.Contests {
		gotC[rawStringField(c, "id")] = true
	}
	if !gotC["c1"] {
		t.Fatalf("контест участницы grp должен прийти: %v", gotC)
	}
}

func TestImportParticipantsOnly(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, selBoth("grp"), true)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	// Импортируем только участников grp, без контестов.
	rep, err := ImportBundle(dst, bundle, sel([]string{"grp"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if rep.ContestsAdded != 0 {
		t.Fatalf("глобальные контесты не должны импортироваться: %d", rep.ContestsAdded)
	}
	if sids := readStrings(t, filepath.Join(dst, "groups", "grp", "group.json"), "student_ids"); len(sids) != 2 {
		t.Fatalf("участники должны добавиться: %v", sids)
	}
	if ids := contestIDs(t, filepath.Join(dst, "groups", "grp", "contests.json")); len(ids) != 0 {
		t.Fatalf("контесты не должны импортироваться: %v", ids)
	}
}

func TestImportContestsOnly(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, selBoth("grp"), true)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	// Только контесты grp, без участников.
	rep, err := ImportBundle(dst, bundle, sel(nil, []string{"grp"}))
	if err != nil {
		t.Fatal(err)
	}
	if rep.StudentsAdded != 0 {
		t.Fatalf("ученики не должны импортироваться: %d", rep.StudentsAdded)
	}
	if !exists(filepath.Join(dst, "students.json")) {
		// students.json могло не создаться — это ок (учеников не импортировали)
	}
	if ids := contestIDs(t, filepath.Join(dst, "groups", "grp", "contests.json")); len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("контесты должны импортироваться: %v", ids)
	}
	if sids := readStrings(t, filepath.Join(dst, "groups", "grp", "group.json"), "student_ids"); len(sids) != 0 {
		t.Fatalf("участников быть не должно: %v", sids)
	}
	if ids := contestIDs(t, filepath.Join(dst, "contests.json")); len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("глобальный контест должен добавиться: %v", ids)
	}
}

func TestImportAppendsToExistingGroup(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, selBoth("grp"), true)
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	write(t, filepath.Join(dst, "students.json"),
		`[{"id":"ivanov","full_name":"Иванов","public_name":"Иванов","accounts":[{"site":"acmp","account_id":"777"}]}]`)
	write(t, filepath.Join(dst, "contests.json"), `[]`)
	write(t, filepath.Join(dst, "groups", "grp", "group.json"),
		`{"title":"Старое название","student_ids":["ivanov"],"group_secret_token":"KEEP-ME"}`)
	write(t, filepath.Join(dst, "groups", "grp", "contests.json"), `[{"id":"old_contest","update":true}]`)

	rep, err := ImportBundle(dst, bundle, Selection{})
	if err != nil {
		t.Fatal(err)
	}

	sids := readStrings(t, filepath.Join(dst, "groups", "grp", "group.json"), "student_ids")
	if len(sids) != 2 || sids[0] != "ivanov" || sids[1] != "petrov" {
		t.Fatalf("student_ids должны дописаться: %v", sids)
	}
	ids := contestIDs(t, filepath.Join(dst, "groups", "grp", "contests.json"))
	if len(ids) != 2 || ids[0] != "old_contest" || ids[1] != "c1" {
		t.Fatalf("контесты должны дописаться в конец: %v", ids)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "groups", "grp", "group.json"))
	if !strings.Contains(string(b), "Старое название") || !strings.Contains(string(b), "KEEP-ME") {
		t.Fatalf("title/token target должны сохраниться: %s", b)
	}
	var students []domain.Student
	sb, _ := os.ReadFile(filepath.Join(dst, "students.json"))
	json.Unmarshal(sb, &students)
	var ivanov domain.Student
	for _, s := range students {
		if s.ID == "ivanov" {
			ivanov = s
		}
	}
	if len(ivanov.Accounts) != 2 {
		t.Fatalf("аккаунты ivanov должны слиться: %+v", ivanov.Accounts)
	}
	if rep.StudentsAdded != 1 || rep.StudentsUpdated != 1 {
		t.Fatalf("отчёт учеников: added=%d updated=%d", rep.StudentsAdded, rep.StudentsUpdated)
	}

	// Повторный импорт — no-op.
	rep2, err := ImportBundle(dst, bundle, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.StudentsAdded != 0 || rep2.ContestsAdded != 0 {
		t.Fatalf("повторный импорт должен быть no-op: %+v", rep2)
	}
	if got := rep2.Groups[0]; got.StudentsAdded != 0 || got.ContestsAdded != 0 {
		t.Fatalf("повторный импорт группы: %+v", got)
	}
}

func TestExportStripToken(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, selBoth("grp"), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range bundle.Groups {
		if g.Slug == "grp" && strings.Contains(string(g.Group), "group_secret_token") {
			t.Fatal("токен должен быть вырезан при includeTokens=false")
		}
	}
}

func TestExportAllWhenSelectionEmpty(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, Selection{}, true) // nil-карты → всё
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, g := range bundle.Groups {
		slugs[g.Slug] = true
	}
	if !slugs["grp"] || !slugs["combo"] {
		t.Fatalf("при пустом выборе экспортируются все группы: %v", slugs)
	}
}

func TestImportRejectsBadSlug(t *testing.T) {
	dst := t.TempDir()
	b := &Bundle{Version: BundleVersion, Groups: []BundleGroup{
		{Slug: "../evil", Group: json.RawMessage(`{"title":"x"}`)},
	}}
	rep, err := ImportBundle(dst, b, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("некорректный slug должен дать предупреждение")
	}
	if exists(filepath.Join(dst, "groups")) {
		t.Fatal("ничего не должно быть записано для битого slug")
	}
}

// readJSON читает JSON-файл в v; помечает fail при ошибке.
func readJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// Полный экспорт → импорт на чистый сервер восстанавливает ручные оценки
// (grades_manual.json) и таблицы кондуитов (manual_tables.json) — оба
// глобальные и inline. Определения контестов остаются чистыми (без table).
func TestRoundTripManualState(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "students.json"),
		`[{"id":"ivanov","full_name":"Иванов Иван","public_name":"Иванов И."}]`)
	// Глобальный кондуит: определение чистое, таблица в отдельном файле.
	write(t, filepath.Join(src, "contests.json"),
		`[{"id":"gk","title":"Общий кондуит","score_system":"edu","source_type":"provider",
		   "provider":"manual_table","provider_config":{"task_count":2}}]`)
	write(t, filepath.Join(src, "manual_tables.json"),
		`{"gk":"ФИО\t1\t2\nИванов Иван\t1\t+\n"}`)
	write(t, filepath.Join(src, "groups", "grp", "group.json"),
		`{"title":"Группа","student_ids":["ivanov"],
		  "grades":{"columns":[{"id":"act","title":"Активность","weight":1,"type":"manual"}]}}`)
	// Ссылка на глобальный кондуит + inline-кондуит.
	write(t, filepath.Join(src, "groups", "grp", "contests.json"),
		`[{"id":"gk","update":true},
		  {"id":"ik","title":"Инлайн","score_system":"edu","source_type":"provider",
		   "provider":"manual_table","provider_config":{"task_count":1},"update":true}]`)
	write(t, filepath.Join(src, "groups", "grp", "manual_tables.json"),
		`{"ik":"ФИО\t1\nИванов Иван\t1\n"}`)
	write(t, filepath.Join(src, "groups", "grp", "grades_manual.json"),
		`{"act":{"ivanov":8.5}}`)

	bundle, err := BuildBundle(src, Selection{}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Явные поля бандла заполнены; определения контестов без таблиц.
	if bundle.ManualTables["gk"] == "" {
		t.Fatalf("bundle.ManualTables must carry gk: %v", bundle.ManualTables)
	}
	for _, c := range bundle.Contests {
		if _, table, ok := legacyTableFromContest(c); ok {
			t.Fatalf("bundle contest defs must be clean, got table %q", table)
		}
	}

	dst := t.TempDir()
	rep, err := ImportBundle(dst, bundle, Selection{})
	if err != nil {
		t.Fatal(err)
	}

	// Глобальная таблица кондуита восстановлена.
	var gt map[string]string
	readJSONFile(t, filepath.Join(dst, "manual_tables.json"), &gt)
	if !strings.Contains(gt["gk"], "Иванов Иван") {
		t.Fatalf("global manual table not restored: %v", gt)
	}
	// contests.json без таблицы в конфиге (проверяем разбором, не подстрокой).
	var defs []json.RawMessage
	readJSONFile(t, filepath.Join(dst, "contests.json"), &defs)
	for _, c := range defs {
		if _, table, ok := legacyTableFromContest(c); ok {
			t.Fatalf("global contest def must be clean, got table %q", table)
		}
	}
	// Inline-таблица группы.
	var it map[string]string
	readJSONFile(t, filepath.Join(dst, "groups", "grp", "manual_tables.json"), &it)
	if !strings.Contains(it["ik"], "Иванов Иван") {
		t.Fatalf("inline table not restored: %v", it)
	}
	// Ручные оценки.
	var mg map[string]map[string]float64
	readJSONFile(t, filepath.Join(dst, "groups", "grp", "grades_manual.json"), &mg)
	if mg["act"]["ivanov"] != 8.5 {
		t.Fatalf("manual grades not restored: %v", mg)
	}
	// Повторный импорт того же бандла — ничего не ломает, оценки те же (max).
	if _, err := ImportBundle(dst, bundle, Selection{}); err != nil {
		t.Fatal(err)
	}
	readJSONFile(t, filepath.Join(dst, "groups", "grp", "grades_manual.json"), &mg)
	if mg["act"]["ivanov"] != 8.5 {
		t.Fatalf("re-import changed grades: %v", mg)
	}
	_ = rep
}

// Слияние в существующее: оценки на целевом и в бандле склеиваются в максимум.
func TestImportMergesManualMax(t *testing.T) {
	// Целевой сервер: у контеста gk уже есть оценки одному ученику.
	dst := t.TempDir()
	write(t, filepath.Join(dst, "students.json"),
		`[{"id":"ivanov","full_name":"Иванов Иван","public_name":"Иванов И."},
		  {"id":"petrov","full_name":"Петров Пётр","public_name":"Петров П."}]`)
	write(t, filepath.Join(dst, "contests.json"),
		`[{"id":"gk","title":"К","score_system":"edu","source_type":"provider",
		   "provider":"manual_table","provider_config":{"task_count":2}}]`)
	write(t, filepath.Join(dst, "manual_tables.json"),
		`{"gk":"ФИО\t1\t2\nИванов Иван\t1\t\n"}`)
	write(t, filepath.Join(dst, "groups", "grp", "group.json"),
		`{"title":"Г","student_ids":["ivanov","petrov"],
		  "grades":{"columns":[{"id":"act","title":"А","weight":1,"type":"manual"}]}}`)
	write(t, filepath.Join(dst, "groups", "grp", "contests.json"), `[{"id":"gk","update":true}]`)
	write(t, filepath.Join(dst, "groups", "grp", "grades_manual.json"), `{"act":{"ivanov":5}}`)

	// Источник: другие/лучшие оценки тем же и новым ученикам.
	src := t.TempDir()
	write(t, filepath.Join(src, "students.json"),
		`[{"id":"ivanov","full_name":"Иванов Иван","public_name":"Иванов И."},
		  {"id":"petrov","full_name":"Петров Пётр","public_name":"Петров П."}]`)
	write(t, filepath.Join(src, "contests.json"),
		`[{"id":"gk","title":"К","score_system":"edu","source_type":"provider",
		   "provider":"manual_table","provider_config":{"task_count":2}}]`)
	write(t, filepath.Join(src, "manual_tables.json"),
		`{"gk":"ФИО\t1\t2\nИванов Иван\t\t1\nПетров Пётр\t1\t\n"}`)
	write(t, filepath.Join(src, "groups", "grp", "group.json"),
		`{"title":"Г","student_ids":["ivanov","petrov"],
		  "grades":{"columns":[{"id":"act","title":"А","weight":1,"type":"manual"}]}}`)
	write(t, filepath.Join(src, "groups", "grp", "contests.json"), `[{"id":"gk","update":true}]`)
	write(t, filepath.Join(src, "groups", "grp", "grades_manual.json"), `{"act":{"ivanov":3,"petrov":9}}`)

	bundle, err := BuildBundle(src, Selection{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(dst, bundle, Selection{}); err != nil {
		t.Fatal(err)
	}

	// Кондуит: Иванов — max("1","")=1 и max("","1")=1; Петров добавлен.
	var gt map[string]string
	readJSONFile(t, filepath.Join(dst, "manual_tables.json"), &gt)
	_, rows := splitForTest(gt["gk"])
	if rows["Иванов Иван"][0] != "1" || rows["Иванов Иван"][1] != "1" {
		t.Fatalf("Иванов merged wrong: %v", rows["Иванов Иван"])
	}
	if rows["Петров Пётр"][0] != "1" {
		t.Fatalf("Петров not merged in: %v", rows)
	}
	// Ручная оценка: max(5,3)=5 у Иванова; Петрову добавлено 9.
	var mg map[string]map[string]float64
	readJSONFile(t, filepath.Join(dst, "groups", "grp", "grades_manual.json"), &mg)
	if mg["act"]["ivanov"] != 5 || mg["act"]["petrov"] != 9 {
		t.Fatalf("grades merge wrong: %v", mg)
	}
}

// splitForTest разбирает TSV в map ФИО -> ячейки (для проверок в тестах).
func splitForTest(tsv string) ([]string, map[string][]string) {
	lines := strings.Split(strings.TrimRight(tsv, "\n"), "\n")
	byName := map[string][]string{}
	var labels []string
	for i, ln := range lines {
		cells := strings.Split(ln, "\t")
		if i == 0 {
			labels = cells[1:]
			continue
		}
		byName[cells[0]] = cells[1:]
	}
	return labels, byName
}

// Бандл v1 (таблица кондуита внутри provider_config) импортируется корректно:
// таблица извлекается в manual_tables.json, определение чистится.
func TestImportLegacyV1Bundle(t *testing.T) {
	dst := t.TempDir()
	bundle := &Bundle{
		Version: 1,
		Contests: []json.RawMessage{json.RawMessage(
			`{"id":"gk","title":"К","score_system":"edu","source_type":"provider",
			  "provider":"manual_table","provider_config":{"task_count":1,"table":"ФИО\t1\nИванов Иван\t+\n"}}`)},
		Groups: []BundleGroup{{
			Slug:  "grp",
			Group: json.RawMessage(`{"title":"Г","student_ids":[]}`),
			Contests: []json.RawMessage{
				json.RawMessage(`{"id":"gk","update":true}`),
				json.RawMessage(`{"id":"ik","title":"Инлайн","score_system":"edu","source_type":"provider","provider":"manual_table","provider_config":{"task_count":1,"table":"ФИО\t1\nИванов Иван\t1\n"},"update":true}`),
			},
		}},
	}
	if _, err := ImportBundle(dst, bundle, Selection{}); err != nil {
		t.Fatal(err)
	}
	// Глобальная таблица извлечена в файл.
	var gt map[string]string
	readJSONFile(t, filepath.Join(dst, "manual_tables.json"), &gt)
	if !strings.Contains(gt["gk"], "Иванов Иван") {
		t.Fatalf("legacy global table not extracted: %v", gt)
	}
	// Inline-таблица извлечена в файл группы.
	var it map[string]string
	readJSONFile(t, filepath.Join(dst, "groups", "grp", "manual_tables.json"), &it)
	if !strings.Contains(it["ik"], "Иванов Иван") {
		t.Fatalf("legacy inline table not extracted: %v", it)
	}
	// Определения контестов очищены.
	var gdefs []json.RawMessage
	readJSONFile(t, filepath.Join(dst, "contests.json"), &gdefs)
	for _, c := range gdefs {
		if _, table, ok := legacyTableFromContest(c); ok {
			t.Fatalf("global def not cleaned: %q", table)
		}
	}
	var cdefs []json.RawMessage
	readJSONFile(t, filepath.Join(dst, "groups", "grp", "contests.json"), &cdefs)
	for _, c := range cdefs {
		if _, table, ok := legacyTableFromContest(c); ok {
			t.Fatalf("group def not cleaned: %q", table)
		}
	}
}

// Раундтрип проверок флагов нечестности: экспорт с участниками группы, ремап
// id по ФИО при импорте, отсутствие перезаписи и чужих групп.
func TestRoundTripFlagReviews(t *testing.T) {
	src := makeSource(t)
	flagKey := domain.CourseFlagKey([]string{"t1", "t2"})
	write(t, filepath.Join(src, "flag_reviews.json"), `{
	 "ivanov|grp|`+flagKey+`":{"at":"2026-07-01T10:00:00Z","comment":"перенос с ejudge","resolution":"transfer",
	   "flag":{"key":"`+flagKey+`","text":"серия","task_urls":["t1","t2"],"at":"2026-06-01T10:00:00Z"}},
	 "ivanov|other|zzz":{"at":"2026-07-01T10:00:00Z","resolution":"violation"}
	}`)

	bundle, err := BuildBundle(src, selBoth("grp"), false)
	if err != nil {
		t.Fatal(err)
	}
	var grp *BundleGroup
	for i := range bundle.Groups {
		if bundle.Groups[i].Slug == "grp" {
			grp = &bundle.Groups[i]
		}
	}
	if grp == nil || len(grp.FlagReviews) != 1 {
		t.Fatalf("в бандле должна быть ровно одна отметка группы grp: %+v", grp)
	}
	br := grp.FlagReviews[0]
	if br.StudentID != "ivanov" || br.FlagKey != flagKey || br.Review.Resolution != "transfer" || br.Review.Flag == nil {
		t.Fatalf("отметка бандла неверна: %+v", br)
	}

	// Экспорт только контестов — отметки не едут (это данные участников).
	contestsOnly, err := BuildBundle(src, sel(nil, []string{"grp"}), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range contestsOnly.Groups {
		if len(g.FlagReviews) != 0 {
			t.Fatalf("contests-only экспорт не должен нести отметки: %+v", g)
		}
	}

	// Целевая директория: тот же ученик (по ФИО) уже существует под ДРУГИМ id.
	dst := t.TempDir()
	write(t, filepath.Join(dst, "students.json"),
		`[{"id":"ivanov-old","full_name":"Иванов","public_name":"Иванов","accounts":[]}]`)
	rep, err := ImportBundle(dst, bundle, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := storage.LoadFlagReviews(dst)
	if err != nil {
		t.Fatal(err)
	}
	wantKey := "ivanov-old|grp|" + flagKey
	rev, ok := got[wantKey]
	if !ok || rev.Resolution != "transfer" || rev.Comment != "перенос с ejudge" || rev.Flag == nil {
		t.Fatalf("отметка должна импортироваться под финальным id: %v", got)
	}
	for k := range got {
		if strings.HasPrefix(k, "ivanov|") || strings.Contains(k, "|other|") {
			t.Fatalf("чужие/неремапленные ключи не должны попадать: %v", got)
		}
	}
	found := false
	for _, g := range rep.Groups {
		if g.Slug == "grp" && g.FlagReviewsAdded == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("отчёт должен показать +1 проверку флагов: %+v", rep.Groups)
	}

	// Существующая отметка не перезаписывается повторным импортом.
	rev.Comment = "локальная правка"
	got[wantKey] = rev
	if err := fileutil.WriteJSON(filepath.Join(dst, "flag_reviews.json"), got, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(dst, bundle, Selection{}); err != nil {
		t.Fatal(err)
	}
	got2, _ := storage.LoadFlagReviews(dst)
	if got2[wantKey].Comment != "локальная правка" {
		t.Fatalf("импорт не должен перезаписывать существующие отметки: %+v", got2[wantKey])
	}
}
