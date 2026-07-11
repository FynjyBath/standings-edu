package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
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
