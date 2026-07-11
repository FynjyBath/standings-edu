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

// source-директория: обычная группа grp + объединённая combo(из grp), 2 ученика,
// 2 глобальных контеста; grp ссылается на 1 контест.
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

func TestRoundTripNewTarget(t *testing.T) {
	src := makeSource(t)
	// Экспортируем только combo — grp должна подтянуться как участница.
	bundle, err := BuildBundle(src, []string{"combo"}, true)
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
	// Ученики grp (ivanov, petrov) включены с Groups=["grp"]; sidorov — нет.
	gotStudents := map[string]bool{}
	for _, s := range bundle.Students {
		gotStudents[s.ID] = true
		if s.ID == "ivanov" && (len(s.Groups) != 1 || s.Groups[0] != "grp") {
			t.Fatalf("ivanov.Groups должно быть [grp], got %v", s.Groups)
		}
	}
	if !gotStudents["ivanov"] || !gotStudents["petrov"] || gotStudents["sidorov"] {
		t.Fatalf("ученики бандла неверны: %v", gotStudents)
	}
	// Контест c1 включён; c2/c3 — нет.
	gotC := map[string]bool{}
	for _, c := range bundle.Contests {
		gotC[rawStringField(c, "id")] = true
	}
	if !gotC["c1"] || gotC["c2"] || gotC["c3"] {
		t.Fatalf("контесты бандла неверны: %v", gotC)
	}

	// Импорт в пустой target.
	dst := t.TempDir()
	rep, err := ImportBundle(dst, bundle)
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
	// Токен перенесён (includeTokens=true).
	b, _ := os.ReadFile(filepath.Join(dst, "groups", "grp", "group.json"))
	if !strings.Contains(string(b), "secret123") {
		t.Fatal("токен должен перенестись при includeTokens=true")
	}
}

func TestImportAppendsToExistingGroup(t *testing.T) {
	src := makeSource(t)
	bundle, err := BuildBundle(src, []string{"grp"}, true)
	if err != nil {
		t.Fatal(err)
	}

	// target уже имеет grp с 1 учеником и другим контестом + свой токен.
	dst := t.TempDir()
	write(t, filepath.Join(dst, "students.json"),
		`[{"id":"ivanov","full_name":"Иванов","public_name":"Иванов","accounts":[{"site":"acmp","account_id":"777"}]}]`)
	write(t, filepath.Join(dst, "contests.json"), `[]`)
	write(t, filepath.Join(dst, "groups", "grp", "group.json"),
		`{"title":"Старое название","student_ids":["ivanov"],"group_secret_token":"KEEP-ME"}`)
	write(t, filepath.Join(dst, "groups", "grp", "contests.json"), `[{"id":"old_contest","update":true}]`)

	rep, err := ImportBundle(dst, bundle)
	if err != nil {
		t.Fatal(err)
	}

	// student_ids: ivanov (был) + petrov (новый) — без дублей, в конец.
	sids := readStrings(t, filepath.Join(dst, "groups", "grp", "group.json"), "student_ids")
	if len(sids) != 2 || sids[0] != "ivanov" || sids[1] != "petrov" {
		t.Fatalf("student_ids должны дописаться: %v", sids)
	}
	// contests: old_contest (был) + c1 (новый) в конце.
	ids := contestIDs(t, filepath.Join(dst, "groups", "grp", "contests.json"))
	if len(ids) != 2 || ids[0] != "old_contest" || ids[1] != "c1" {
		t.Fatalf("контесты должны дописаться в конец: %v", ids)
	}
	// Название и токен target сохранены (не перезаписаны).
	b, _ := os.ReadFile(filepath.Join(dst, "groups", "grp", "group.json"))
	if !strings.Contains(string(b), "Старое название") || !strings.Contains(string(b), "KEEP-ME") {
		t.Fatalf("title/token target должны сохраниться: %s", b)
	}
	// Ученик ivanov: аккаунты слиты (acmp + codeforces).
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

	// Повторный импорт — ничего не добавляется.
	rep2, err := ImportBundle(dst, bundle)
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
	bundle, err := BuildBundle(src, []string{"grp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range bundle.Groups {
		if g.Slug == "grp" && strings.Contains(string(g.Group), "group_secret_token") {
			t.Fatal("токен должен быть вырезан при includeTokens=false")
		}
	}
}

func TestImportRejectsBadSlug(t *testing.T) {
	dst := t.TempDir()
	b := &Bundle{Version: BundleVersion, Groups: []BundleGroup{
		{Slug: "../evil", Group: json.RawMessage(`{"title":"x"}`)},
	}}
	rep, err := ImportBundle(dst, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("некорректный slug должен дать предупреждение")
	}
	if _, err := os.Stat(filepath.Join(dst, "groups")); err == nil {
		t.Fatal("ничего не должно быть записано для битого slug")
	}
}
