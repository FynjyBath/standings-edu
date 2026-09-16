package studentintake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"standings-edu/internal/domain"
)

func queueSetup(t *testing.T, queue string) (*Store, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dataDir, "students.json"), `[]`)
	for _, slug := range []string{"g1", "g2"} {
		write(filepath.Join(dataDir, "groups", slug, "group.json"),
			`{"title":"`+slug+`","student_ids":[],"accesses":[{"id":"a","title":"A","auth":"token","token":"t","perms":["view.unfrozen"]}]}`)
	}
	queuePath := filepath.Join(dataDir, "student_intake.json")
	write(queuePath, queue)
	stagingPath := filepath.Join(dataDir, "student_intake_admin.json")
	return NewStore(queuePath), dataDir, stagingPath
}

func queueNames(t *testing.T, s *Store, staging string) []string {
	t.Helper()
	q, err := s.IntakeQueue(staging)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(q))
	for _, a := range q {
		out = append(out, a.FullName)
	}
	return out
}

func groupIDs(t *testing.T, dataDir, slug string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dataDir, "groups", slug, "group.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gf domain.GroupFile
	if err := json.Unmarshal(body, &gf); err != nil {
		t.Fatal(err)
	}
	return gf.StudentIDs
}

// Приём выборки: взяли отмеченных, остальные остались в очереди.
func TestAcceptIntakeSelection(t *testing.T) {
	s, dataDir, staging := queueSetup(t, `[
	  {"full_name":"Аня Первая","groups":["g1"]},
	  {"full_name":"Боря Второй","groups":["g1"]},
	  {"full_name":"Вера Третья","groups":["g1"]}]`)

	stats, err := s.AcceptIntake(dataDir, staging, IntakeSelection{FullNames: []string{"Аня Первая"}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Accepted != 1 || stats.Created != 1 || stats.Remaining != 2 {
		t.Fatalf("статистика: %+v", stats)
	}
	got := queueNames(t, s, staging)
	if len(got) != 2 || got[0] != "Боря Второй" || got[1] != "Вера Третья" {
		t.Fatalf("в очереди должны остаться невыбранные, а там: %v", got)
	}
	if ids := groupIDs(t, dataDir, "g1"); len(ids) != 1 {
		t.Fatalf("в группе должен быть один ученик: %v", ids)
	}
}

// Анкета в две группы: приём первой оставляет её ждать вторую, ученик один.
func TestAcceptIntakeKeepsOtherGroups(t *testing.T) {
	s, dataDir, staging := queueSetup(t, `[{"full_name":"Оба Сразу","groups":["g1","g2"]}]`)

	if _, err := s.AcceptIntake(dataDir, staging, IntakeSelection{Group: "g1"}); err != nil {
		t.Fatal(err)
	}
	q, err := s.IntakeQueue(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 || len(q[0].Groups) != 1 || q[0].Groups[0] != "g2" {
		t.Fatalf("анкета должна остаться только для g2: %+v", q)
	}

	if _, err := s.AcceptIntake(dataDir, staging, IntakeSelection{Group: "g2"}); err != nil {
		t.Fatal(err)
	}
	if got := queueNames(t, s, staging); len(got) != 0 {
		t.Fatalf("очередь должна опустеть: %v", got)
	}
	students, err := LoadStudentsFile(filepath.Join(dataDir, "students.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(students) != 1 || len(students[0].Groups) != 2 {
		t.Fatalf("ожидалась одна запись в двух группах: %+v", students)
	}
	if len(groupIDs(t, dataDir, "g1")) != 1 || len(groupIDs(t, dataDir, "g2")) != 1 {
		t.Fatal("ученик должен попасть в обе группы")
	}
}

// Группа принимает только свои анкеты и не трогает чужие.
func TestAcceptIntakeGroupScope(t *testing.T) {
	s, dataDir, staging := queueSetup(t, `[
	  {"full_name":"Своя Анкета","groups":["g1"]},
	  {"full_name":"Чужая Анкета","groups":["g2"]},
	  {"full_name":"Ничейная Анкета","groups":[]}]`)

	stats, err := s.AcceptIntake(dataDir, staging, IntakeSelection{Group: "g1"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Accepted != 1 {
		t.Fatalf("группа должна принять ровно свою анкету: %+v", stats)
	}
	got := queueNames(t, s, staging)
	if len(got) != 2 {
		t.Fatalf("чужая и ничейная анкеты должны остаться: %v", got)
	}
}

// Превью ничего не пишет и показывает конфликт аккаунта.
func TestPreviewIntakeDoesNotWrite(t *testing.T) {
	s, dataDir, staging := queueSetup(t, `[{"full_name":"Новый Ученик","groups":["g1"],
	  "accounts":[{"site":"codeforces","account_id":"dup"}]}]`)
	if err := os.WriteFile(filepath.Join(dataDir, "students.json"),
		[]byte(`[{"id":"old","full_name":"Старый Ученик","accounts":[{"site":"codeforces","account_id":"dup"}]}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	preview, err := s.PreviewIntake(dataDir, staging, IntakeSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Added != 1 || len(preview.Students) != 1 {
		t.Fatalf("превью: %+v", preview)
	}
	if len(preview.Students[0].Conflicts) == 0 {
		t.Error("ожидался конфликт: аккаунт уже у другого ученика")
	}
	if got := queueNames(t, s, staging); len(got) != 1 {
		t.Fatalf("превью не должно трогать очередь: %v", got)
	}
	students, _ := LoadStudentsFile(filepath.Join(dataDir, "students.json"))
	if len(students) != 1 {
		t.Fatalf("превью не должно писать students.json: %+v", students)
	}
}

// Правка анкеты до приёма; со стороны группы список групп не меняется.
func TestUpdateIntakeEntry(t *testing.T) {
	s, dataDir, staging := queueSetup(t, `[{"full_name":"Опечатка Втфио","groups":["g1","g2"]}]`)

	err := s.UpdateIntakeEntry(staging, "Опечатка Втфио", domain.Student{
		FullName: "Правильное ФИО", PublicName: "Правильное П.",
		Accounts: []domain.Account{{Site: "codeforces", AccountID: "fixed"}},
		Groups:   []string{"g1"},
	}, "g1")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := s.IntakeQueue(staging)
	if q[0].FullName != "Правильное ФИО" || len(q[0].Accounts) != 1 {
		t.Fatalf("правка не применилась: %+v", q[0])
	}
	// id выводится из ФИО: после правки опечатки он не должен её унаследовать.
	if q[0].ID != "" {
		t.Errorf("после переименования id должен сброситься, а он %q", q[0].ID)
	}
	if len(q[0].Groups) != 2 {
		t.Fatalf("группа не должна менять список групп анкеты: %+v", q[0].Groups)
	}

	// Админ (allowedGroup=="") группы менять может.
	if err := s.UpdateIntakeEntry(staging, "Правильное ФИО", domain.Student{
		FullName: "Правильное ФИО", Groups: []string{"g2"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	q, _ = s.IntakeQueue(staging)
	if len(q[0].Groups) != 1 || q[0].Groups[0] != "g2" {
		t.Fatalf("админ должен менять группы: %+v", q[0].Groups)
	}

	// Чужую анкету группа не правит.
	if err := s.UpdateIntakeEntry(staging, "Правильное ФИО", domain.Student{FullName: "Взлом"}, "g1"); err == nil {
		t.Error("правка чужой анкеты должна отклоняться")
	}
	_ = dataDir
}

// Убрать анкету: админ — целиком, группа — только из своей группы.
func TestDiscardIntakeEntry(t *testing.T) {
	s, _, staging := queueSetup(t, `[
	  {"full_name":"В Двух","groups":["g1","g2"]},
	  {"full_name":"Только g1","groups":["g1"]}]`)

	if err := s.DiscardIntakeEntry(staging, "В Двух", "g1"); err != nil {
		t.Fatal(err)
	}
	q, _ := s.IntakeQueue(staging)
	var both *domain.Student
	for i := range q {
		if q[i].FullName == "В Двух" {
			both = &q[i]
		}
	}
	if both == nil || len(both.Groups) != 1 || both.Groups[0] != "g2" {
		t.Fatalf("у анкеты должна остаться только g2: %+v", q)
	}

	if err := s.DiscardIntakeEntry(staging, "В Двух", ""); err != nil {
		t.Fatal(err)
	}
	if got := queueNames(t, s, staging); len(got) != 1 || got[0] != "Только g1" {
		t.Fatalf("админ убирает анкету целиком: %v", got)
	}
}

// Остатки старого файла-пачки вливаются в очередь при первом чтении.
func TestStagingFoldedIntoQueue(t *testing.T) {
	s, _, staging := queueSetup(t, `[{"full_name":"Свежая Анкета","groups":["g1"]}]`)
	if err := os.WriteFile(staging,
		[]byte(`[{"full_name":"Застрявшая Анкета","groups":["g1"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := queueNames(t, s, staging)
	if len(got) != 2 {
		t.Fatalf("обе анкеты должны быть в очереди: %v", got)
	}
	body, err := os.ReadFile(staging)
	if err != nil {
		t.Fatal(err)
	}
	if !isEmptyIntakeFile(body) {
		t.Fatalf("файл-пачка должен быть очищен: %s", body)
	}
	// Повторное чтение ничего не дублирует.
	if got := queueNames(t, s, staging); len(got) != 2 {
		t.Fatalf("после вливания очередь не должна расти: %v", got)
	}
}
