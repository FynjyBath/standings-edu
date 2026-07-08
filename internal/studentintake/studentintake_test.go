package studentintake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
)

// Сценарий «ответ формы → merge intake»: AddStudentsToGroups дописывает ученика
// в group.json. Grades (включая normalize и round) не должны портиться —
// регрессия: после merge группа ломалась на "normalize".
func TestAddStudentsToGroupsKeepsGrades(t *testing.T) {
	dataDir := t.TempDir()
	groupDir := filepath.Join(dataDir, "groups", "smip_2026_p3")
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	groupJSON := `{
  "title": "П3 - Искатели",
  "form_link": "https://forms.yandex.ru/u/abc",
  "update": true,
  "student_ids": ["voron-ea"],
  "grades": {
    "title": "Оценки",
    "round": 1,
    "columns": [
      {"id": "educational", "title": "Тематические", "weight": 0.35, "type": "table", "table_name": "Тематические", "metric": "plus", "normalize": "max"},
      {"id": "zachet", "title": "Зачет", "weight": 0.3, "type": "manual"}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(groupDir, "group.json"), []byte(groupJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	merged := []domain.Student{
		{ID: "voron-ea", FullName: "Ворон Егор Андреевич"},
		{ID: "kaleev-ve", FullName: "Калеев Владислав Евгеньевич"},
	}
	intake := []domain.Student{
		{FullName: "Калеев Владислав Евгеньевич", Groups: []string{"smip_2026_p3"}},
	}

	if err := AddStudentsToGroups(dataDir, merged, intake); err != nil {
		t.Fatalf("AddStudentsToGroups: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(groupDir, "group.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Файл должен читаться и генератором, и повторным merge.
	var gf domain.GroupFile
	if err := json.Unmarshal(body, &gf); err != nil {
		t.Fatalf("group.json broken after merge: %v; body=%s", err, body)
	}
	if len(gf.StudentIDs) != 2 || gf.StudentIDs[0] != "voron-ea" || gf.StudentIDs[1] != "kaleev-ve" {
		t.Fatalf("student not added: %v", gf.StudentIDs)
	}
	if gf.Grades == nil || len(gf.Grades.Columns) != 2 || gf.Grades.Round == nil || *gf.Grades.Round != 1 {
		t.Fatalf("grades lost after merge: %s", body)
	}
	if gf.Grades.Columns[0].Normalize.Mode != domain.NormalizeMax {
		t.Fatalf("normalize lost after merge: %s", body)
	}
	if strings.Contains(string(body), `"Mode"`) {
		t.Fatalf("normalize serialized as struct: %s", body)
	}
	if gf.FormLink == "" || gf.Update == nil || !*gf.Update {
		t.Fatalf("form_link/update lost: %s", body)
	}

	// Повторный merge того же ученика — идемпотентно и без порчи.
	if err := AddStudentsToGroups(dataDir, merged, intake); err != nil {
		t.Fatalf("second AddStudentsToGroups: %v", err)
	}
	body2, _ := os.ReadFile(filepath.Join(groupDir, "group.json"))
	var gf2 domain.GroupFile
	if err := json.Unmarshal(body2, &gf2); err != nil {
		t.Fatalf("group.json broken after second merge: %v", err)
	}
	if len(gf2.StudentIDs) != 2 {
		t.Fatalf("duplicate student after second merge: %v", gf2.StudentIDs)
	}
}

// Приём анкеты (RPC Submit) пишет только в intake-файл и не трогает группы.
func TestSubmitWritesOnlyIntake(t *testing.T) {
	dir := t.TempDir()
	intakePath := filepath.Join(dir, "student_intake.json")
	store := NewStore(intakePath)

	student, err := store.Submit(map[string]string{
		"full_name":   "Калеев Владислав Евгеньевич",
		"public_name": "Калеев В. Е.",
		"group":       "smip_2026_p3",
		"informatics": "904688",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if student.FullName != "Калеев Владислав Евгеньевич" {
		t.Fatalf("unexpected student: %+v", student)
	}

	items, err := LoadIntakeFile(intakePath)
	if err != nil {
		t.Fatalf("load intake: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 intake item, got %d", len(items))
	}
	got := domain.NormalizeStudent(items[0])
	if len(got.Groups) != 1 || got.Groups[0] != "smip_2026_p3" {
		t.Fatalf("group not recorded: %+v", got)
	}
	found := false
	for _, acc := range got.Accounts {
		if acc.Site == "informatics" && acc.AccountID == "904688" {
			found = true
		}
	}
	if !found {
		t.Fatalf("informatics account not recorded: %+v", got.Accounts)
	}

	// Повторная отправка той же анкеты (обновление аккаунта) — не дубль.
	if _, err := store.Submit(map[string]string{
		"full_name": "Калеев Владислав Евгеньевич",
		"group":     "smip_2026_p3",
		"acmp":      "12345",
	}); err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	items, _ = LoadIntakeFile(intakePath)
	if len(items) != 1 {
		t.Fatalf("duplicate intake item: %d", len(items))
	}
}

// Пробный merge (dry-run): разрешение анкет в учеников и привязка к группам,
// без записи на диск.
func TestBuildMergePreview(t *testing.T) {
	dir := t.TempDir()
	// существующая группа: в ней уже voron-ea.
	groupDir := filepath.Join(dir, "groups", "smip_2026_p3")
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groupDir, "group.json"),
		[]byte(`{"title":"П3","student_ids":["voron-ea"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	existing := []domain.Student{{ID: "voron-ea", FullName: "Ворон Егор Андреевич"}}
	intake := []domain.Student{
		{FullName: "Ворон Егор Андреевич", Groups: []string{"smip_2026_p3"}, Accounts: []domain.Account{{Site: "acmp", AccountID: "1"}}}, // обновление, уже в группе
		{FullName: "Калеев Владислав Евгеньевич", Groups: []string{"smip_2026_p3"}},                                                      // новый, добавится в группу
		{FullName: "Смирнова Елена", Groups: []string{"new_group"}},                                                                      // новый, новая группа
	}

	preview, err := BuildMergePreview(dir, existing, intake)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Added != 2 || preview.Updated != 1 {
		t.Fatalf("stats wrong: %+v", preview)
	}
	if len(preview.Students) != 3 {
		t.Fatalf("students len: %d", len(preview.Students))
	}
	// Ворон: обновление, уже в группе.
	v := preview.Students[0]
	if v.IsNew || v.FinalID != "voron-ea" || len(v.Groups) != 1 || !v.Groups[0].AlreadyMember {
		t.Fatalf("voron preview wrong: %+v", v)
	}
	// Калеев: новый, добавится (не член).
	k := preview.Students[1]
	if !k.IsNew || k.FinalID == "" || len(k.Groups) != 1 || k.Groups[0].AlreadyMember {
		t.Fatalf("kaleev preview wrong: %+v", k)
	}
	// Ничего не записалось: group.json неизменён.
	body, _ := os.ReadFile(filepath.Join(groupDir, "group.json"))
	if strings.Contains(string(body), "kaleev") {
		t.Fatalf("dry-run must not write: %s", body)
	}
}

// Коллизия аккаунтов: одна учётка попала двум разным людям — в превью
// появляется предупреждение с указанием другого ученика.
func TestBuildMergePreviewAccountConflicts(t *testing.T) {
	dir := t.TempDir()
	existing := []domain.Student{
		{ID: "alice", FullName: "Алиса Иванова", Accounts: []domain.Account{{Site: "codeforces", AccountID: "tourist"}}},
	}
	intake := []domain.Student{
		// Другой человек указал тот же codeforces-аккаунт (регистр другой) — коллизия с alice.
		{FullName: "Борис Петров", Accounts: []domain.Account{{Site: "codeforces", AccountID: "Tourist"}, {Site: "acmp", AccountID: "5"}}},
		// Третий указал acmp:5 — коллизия с Борисом (в рамках того же батча).
		{FullName: "Виктор Сидоров", Accounts: []domain.Account{{Site: "acmp", AccountID: "5"}}},
	}

	preview, err := BuildMergePreview(dir, existing, intake)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	boris := preview.Students[0]
	// Борис: codeforces:Tourist совпал с Алисой, acmp:5 — с Виктором.
	if len(boris.Conflicts) != 2 {
		t.Fatalf("boris must have 2 conflicts: %+v", boris.Conflicts)
	}
	names := map[string]bool{}
	for _, c := range boris.Conflicts {
		names[c.OtherName] = true
	}
	if !names["Алиса Иванова"] || !names["Виктор Сидоров"] {
		t.Fatalf("boris conflict names wrong: %+v", boris.Conflicts)
	}
	// Виктор: acmp:5 совпал с Борисом.
	viktor := preview.Students[1]
	if len(viktor.Conflicts) != 1 || viktor.Conflicts[0].OtherName != "Борис Петров" {
		t.Fatalf("viktor conflict wrong: %+v", viktor.Conflicts)
	}
}

func TestParseIntakeBytes(t *testing.T) {
	items, err := ParseIntakeBytes([]byte(`[{"full_name":"Иван Иванов","group":"g1","acmp":"5"}]`))
	if err != nil || len(items) != 1 {
		t.Fatalf("parse: %v len=%d", err, len(items))
	}
	if _, err := ParseIntakeBytes([]byte(`{not json`)); err == nil {
		t.Fatal("invalid json must fail")
	}
	if _, err := ParseIntakeBytes([]byte(`[{"group":"g1"}]`)); err == nil {
		t.Fatal("empty full_name must fail")
	}
}
