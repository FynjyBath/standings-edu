package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"standings-edu/internal/domain"
)

func TestPlanFIORegistration(t *testing.T) {
	students := []domain.Student{
		{ID: "ivanov", FullName: "Иванов Иван Иванович"},
		{ID: "petrova", FullName: "Петрова Анна Андреевна"},
		{ID: "dup1", FullName: "Смирнов Пётр"}, // однофамильцы-тёзки
		{ID: "dup2", FullName: "Смирнов Пётр"},
	}
	members := map[string]struct{}{"petrova": {}} // Петрова уже в группе

	names := "" +
		"Иванов Иван Иванович\n" + // существующий, не в группе → add
		"петрова  анна   андреевна\n" + // тот же, что petrova (регистр/пробелы) → already
		"Сидоров Сидор\n" + // новый → create
		"Сидоров Сидор\n" + // повтор в списке → duplicate
		"Смирнов Пётр\n" + // неоднозначно (2 совпадения) → ambiguous
		"   \n" + // пустая — пропускается
		"Ёжиков Пётр\n" // новый (ё) → create

	plan := planFIORegistration(students, members, names)

	if plan.Total != 6 { // пустая строка не считается
		t.Fatalf("total=%d want 6", plan.Total)
	}
	byInput := map[string]FIORegRow{}
	for _, r := range plan.Rows {
		byInput[r.Input] = r
	}
	checks := map[string]struct {
		status, sid string
	}{
		"Иванов Иван Иванович":      {fioStatusAdd, "ivanov"},
		"петрова  анна   андреевна": {fioStatusAlready, "petrova"},
		"Смирнов Пётр":              {fioStatusAmbiguous, ""},
		"Ёжиков Пётр":               {fioStatusCreate, ""},
	}
	for input, want := range checks {
		got := byInput[input]
		if got.Status != want.status || got.StudentID != want.sid {
			t.Errorf("%q → {%s,%s}, want {%s,%s}", input, got.Status, got.StudentID, want.status, want.sid)
		}
	}
	// Второй "Сидоров Сидор" — дубликат.
	dupCount := 0
	for _, r := range plan.Rows {
		if r.Status == fioStatusDuplicate {
			dupCount++
		}
	}
	if dupCount != 1 {
		t.Fatalf("duplicate rows=%d want 1", dupCount)
	}
	if plan.Create != 2 || plan.Add != 1 || plan.Already != 1 || plan.Warnings != 2 {
		t.Fatalf("counts: create=%d add=%d already=%d warn=%d", plan.Create, plan.Add, plan.Already, plan.Warnings)
	}
}

// Применение: создаёт новых, дописывает существующих в группу, повторный запуск
// ничего не дублирует (идемпотентность).
func TestRegisterNamesApply(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	h.ConfigureSourceDir(dataDir)
	mustWrite := func(rel, v string) {
		t.Helper()
		p := filepath.Join(dataDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("students.json", `[{"id":"ivanov","full_name":"Иванов Иван","public_name":"Иванов И.","accounts":[{"site":"codeforces","account_id":"x"}]}]`)
	mustWrite("groups/g1/group.json", `{"title":"Г","student_ids":[]}`)

	apply := func(names string) map[string]any {
		body, _ := json.Marshal(map[string]string{"slug": "g1", "names": names})
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.AdminGroupRegisterNamesApply(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if rec.Code != http.StatusOK || resp["ok"] != true {
			t.Fatalf("apply: code=%d resp=%v", rec.Code, resp)
		}
		return resp
	}

	names := "Иванов Иван\nПетров Пётр\n"
	apply(names)

	// students.json: добавился Петров с пустыми аккаунтами; Иванов не тронут.
	var students []domain.Student
	b, _ := os.ReadFile(filepath.Join(dataDir, "students.json"))
	_ = json.Unmarshal(b, &students)
	if len(students) != 2 {
		t.Fatalf("students=%d want 2: %+v", len(students), students)
	}
	var petrov *domain.Student
	for i := range students {
		if students[i].FullName == "Петров Пётр" {
			petrov = &students[i]
		}
		if students[i].ID == "ivanov" && len(students[i].Accounts) != 1 {
			t.Fatal("существующий Иванов не должен терять аккаунты")
		}
	}
	if petrov == nil || len(petrov.Accounts) != 0 || petrov.PublicName == "" {
		t.Fatalf("новый Петров: %+v", petrov)
	}
	// group.json: оба в группе.
	var gf domain.GroupFile
	b, _ = os.ReadFile(filepath.Join(dataDir, "groups", "g1", "group.json"))
	_ = json.Unmarshal(b, &gf)
	if len(gf.StudentIDs) != 2 {
		t.Fatalf("group ids=%v want 2", gf.StudentIDs)
	}

	// Повторный запуск того же списка — без дублей и без новых учеников.
	apply(names)
	b, _ = os.ReadFile(filepath.Join(dataDir, "students.json"))
	_ = json.Unmarshal(b, &students)
	if len(students) != 2 {
		t.Fatalf("повторный apply не должен создавать дублей: %d", len(students))
	}
	b, _ = os.ReadFile(filepath.Join(dataDir, "groups", "g1", "group.json"))
	_ = json.Unmarshal(b, &gf)
	if len(gf.StudentIDs) != 2 {
		t.Fatalf("повторный apply не должен дублировать в группе: %v", gf.StudentIDs)
	}
}
