package source

import (
	"context"
	"encoding/json"
	"testing"

	"standings-edu/internal/domain"
)

func TestParseManualCell(t *testing.T) {
	one, half, zero, five := 1, 1, 0, 5
	_ = half
	cases := []struct {
		in     string
		status string
		score  *int
	}{
		{"", domain.TaskStatusNone, nil},
		{".", domain.TaskStatusNone, nil},
		{"-", domain.TaskStatusNone, nil},
		{"—", domain.TaskStatusNone, nil},
		{"+", domain.TaskStatusSolved, &one},
		{"±", domain.TaskStatusAttempted, nil},
		{"1", domain.TaskStatusSolved, &one},
		{"5", domain.TaskStatusSolved, &five},
		{"0", domain.TaskStatusAttempted, &zero},
		{"0,5", domain.TaskStatusSolved, &one}, // запятая + округление
		{"н", domain.TaskStatusNone, nil},      // мусорный текст игнорируется
	}
	for _, c := range cases {
		got := parseManualCell(c.in)
		if got.status != c.status {
			t.Errorf("cell %q status=%q want %q", c.in, got.status, c.status)
		}
		switch {
		case c.score == nil && got.score != nil:
			t.Errorf("cell %q score=%d want nil", c.in, *got.score)
		case c.score != nil && (got.score == nil || *got.score != *c.score):
			t.Errorf("cell %q score=%v want %d", c.in, got.score, *c.score)
		}
	}
}

func TestParseManualTableHeaderAndPadding(t *testing.T) {
	// Заголовок с «ФИО», у второй строки не хватает хвостовых ячеек.
	labels, rows, err := parseManualTable("ФИО\tЗадача 1\tЗадача 2\tИтог\nИванов Иван\t1\t\t2\nПетров Пётр\t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 3 || labels[0] != "Задача 1" || labels[2] != "Итог" {
		t.Fatalf("labels: %v", labels)
	}
	if len(rows) != 2 || len(rows[1].cells) != 3 {
		t.Fatalf("rows/padding: %+v", rows)
	}
	if rows[1].cells[1].status != domain.TaskStatusNone {
		t.Fatalf("padded cell must be none: %+v", rows[1].cells)
	}

	// Без заголовка — колонки нумеруются.
	labels, rows, err = parseManualTable("Иванов Иван\t1\t\t1\nПетров Пётр\t\t1\t")
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 3 || labels[0] != "1" || labels[2] != "3" {
		t.Fatalf("numbered labels: %v", labels)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}

	// Без табуляций — понятная ошибка.
	if _, _, err := parseManualTable("Иванов Иван 1 1"); err == nil {
		t.Fatal("must fail without tabs")
	}
	if _, _, err := parseManualTable("   \n \n"); err == nil {
		t.Fatal("must fail on empty table")
	}
}

// Кондуит целиком: матчинг по ФИО (обратный порядок), плюсики в edu,
// баллы в ioi, show_all добавляет несопоставленных.
func TestManualTableProviderBuildStandings(t *testing.T) {
	provider := NewManualTableProvider()
	table := "ФИО\t1\t2\t3\n" +
		"Иван Иванов\t1\t\t1\n" + // имя-фамилия наоборот
		"Пётр Петров\t\t0,5\t\n" +
		"Чужой Человек\t1\t1\t1\n"
	students := []domain.Student{
		{ID: "ivanov", FullName: "Иванов Иван Петрович", PublicName: "Иванов И."},
		{ID: "petrov", FullName: "Петров Пётр", PublicName: "Петров П."},
		{ID: "absent", FullName: "Отсутствующий Ученик", PublicName: "Отсутствующий У."},
	}

	build := func(score domain.ScoreSystem, showAll bool) domain.GeneratedContestStandings {
		cfgJSON, _ := json.Marshal(map[string]any{"table": table, "show_all": showAll})
		out, err := provider.BuildStandings(context.Background(), ContestProviderInput{
			Contest: domain.Contest{
				ID: "manual1", Title: "Кондуит", ScoreSystem: score,
				ContestType: domain.ContestTypeProvider, Provider: ManualTableProviderID,
				ProviderConfig: cfgJSON,
			},
			Students: students,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// edu: плюсики.
	out := build(domain.ScoreSystemEdu, false)
	if len(out.Rows) != 3 {
		t.Fatalf("rows=%d want 3 (без show_all)", len(out.Rows))
	}
	byID := map[string]domain.GeneratedRow{}
	for _, r := range out.Rows {
		byID[r.StudentID] = r
	}
	if r := byID["ivanov"]; r.SolvedCount != 2 || r.Statuses[0] != domain.TaskStatusSolved || r.Statuses[1] != domain.TaskStatusNone {
		t.Fatalf("ivanov edu row: %+v", r)
	}
	if r := byID["petrov"]; r.SolvedCount != 1 || r.Statuses[1] != domain.TaskStatusSolved {
		t.Fatalf("petrov edu row: %+v", r)
	}
	if r := byID["absent"]; r.SolvedCount != 0 || r.Statuses[0] != domain.TaskStatusNone {
		t.Fatalf("absent edu row: %+v", r)
	}
	if len(out.Tasks) != 3 || out.Tasks[0].Label != "1" || out.Tasks[0].URL != "" {
		t.Fatalf("tasks: %+v", out.Tasks)
	}

	// ioi + show_all: баллы и чужая строка.
	out = build(domain.ScoreSystemIOI, true)
	if len(out.Rows) != 4 {
		t.Fatalf("rows=%d want 4 (show_all)", len(out.Rows))
	}
	names := map[string]domain.GeneratedRow{}
	for _, r := range out.Rows {
		names[r.PublicName] = r
	}
	if r := names["Иванов И."]; r.TotalScore != 2 || r.Scores[0] == nil || *r.Scores[0] != 1 {
		t.Fatalf("ivanov ioi row: %+v", r)
	}
	if r := names["Чужой Человек"]; r.TotalScore != 3 || r.SolvedCount != 3 {
		t.Fatalf("extra row: %+v", r)
	}
	// Сортировка: Чужой (3) выше Иванова (2) выше Петрова (1).
	if out.Rows[0].PublicName != "Чужой Человек" || out.Rows[1].StudentID != "ivanov" {
		t.Fatalf("sort: %v, %v", out.Rows[0].PublicName, out.Rows[1].PublicName)
	}
}
