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
	labels, rows, err := parseManualTable("ФИО\tЗадача 1\tЗадача 2\tИтог\nИванов Иван\t1\t\t2\nПетров Пётр\t1", 0)
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
	labels, rows, err = parseManualTable("Иванов Иван\t1\t\t1\nПетров Пётр\t\t1\t", 0)
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
	if _, _, err := parseManualTable("Иванов Иван 1 1", 0); err == nil {
		t.Fatal("must fail without tabs")
	}
	if _, _, err := parseManualTable("   \n \n", 0); err == nil {
		t.Fatal("must fail on empty table")
	}
}

// task_count фиксирует число колонок: строки обрезаются/дополняются, пустая
// таблица допустима (кондуит «на будущее»), лишние колонки игнорируются.
func TestParseManualTableFixedTaskCount(t *testing.T) {
	// Обрезка и дополнение до 2 колонок.
	labels, rows, err := parseManualTable("Иванов Иван\t1\t1\t1\nПетров Пётр\t1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 || len(rows[0].cells) != 2 || len(rows[1].cells) != 2 {
		t.Fatalf("fixed cols: labels=%v rows=%+v", labels, rows)
	}
	if rows[1].cells[1].status != domain.TaskStatusNone {
		t.Fatalf("padded cell must be none: %+v", rows[1].cells)
	}

	// Пустая таблица с task_count — колонки есть, строк нет.
	labels, rows, err = parseManualTable("", 3)
	if err != nil || len(labels) != 3 || len(rows) != 0 {
		t.Fatalf("empty with task_count: %v %v %v", labels, rows, err)
	}

	// Только заголовок с task_count — тоже ок, метки из заголовка.
	labels, rows, err = parseManualTable("ФИО\tА\tБ\tВ", 3)
	if err != nil || len(rows) != 0 || labels[0] != "А" || labels[2] != "В" {
		t.Fatalf("header-only with task_count: %v %v %v", labels, rows, err)
	}

	// Провайдер с пустой таблицей и task_count: пустые строки учеников.
	provider := NewManualTableProvider()
	cfgJSON, _ := json.Marshal(map[string]any{"table": "", "task_count": 3})
	out, err := provider.BuildStandings(context.Background(), ContestProviderInput{
		Contest:  domain.Contest{ID: "m", ScoreSystem: domain.ScoreSystemEdu, Provider: ManualTableProviderID, ProviderConfig: cfgJSON},
		Students: []domain.Student{{ID: "s1", FullName: "Иванов Иван", PublicName: "Иванов И."}},
	})
	if err != nil {
		t.Fatalf("empty konduit: %v", err)
	}
	if len(out.Tasks) != 3 || len(out.Rows) != 1 || out.Rows[0].Statuses[2] != domain.TaskStatusNone {
		t.Fatalf("empty konduit build: tasks=%d rows=%+v", len(out.Tasks), out.Rows)
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

func TestMergeManualTablesMax(t *testing.T) {
	// Разные ученики, пересечение по одному — максимум по ячейкам.
	a := "ФИО\t1\t2\t3\nИванов Иван\t1\t\t+\nПетров Пётр\t\t1\t\n"
	b := "ФИО\t1\t2\t3\nИванов Иван\t\t1\t2\nСидоров С\t1\t1\t1\n"
	got := MergeManualTablesMax(a, b)
	_, rows := SplitManualTable(got, 0)
	byName := map[string][]string{}
	for _, r := range rows {
		byName[r[0]] = r[1:]
	}
	// Иванов: max("1","") , max("","1") , max("+","2")=2
	if v := byName["Иванов Иван"]; v[0] != "1" || v[1] != "1" || v[2] != "2" {
		t.Fatalf("Иванов merge: %v", v)
	}
	if _, ok := byName["Петров Пётр"]; !ok {
		t.Fatal("строка только из A должна остаться")
	}
	if _, ok := byName["Сидоров С"]; !ok {
		t.Fatal("строка только из B должна остаться")
	}
	// Идемпотентность: повторное слияние того же результата ничего не меняет.
	if again := MergeManualTablesMax(got, got); again != got {
		t.Fatalf("merge не идемпотентен:\n%q\n%q", got, again)
	}
	// Слияние с пустой таблицей — нормализованная копия непустой (идемпотентно).
	if MergeManualTablesMax(got, "") != got {
		t.Fatal("слияние с пустой должно вернуть ту же таблицу")
	}
	if MergeManualTablesMax("", "") != "" {
		t.Fatal("две пустые → пусто")
	}
}

func TestManualCellMax(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "1", "1"},
		{"1", "", "1"},
		{"", "", ""},
		{"1", "2", "2"},
		{"2", "1", "2"},
		{"+", "1", "+"}, // равный ранг (1) → первая
		{"+", "5", "5"}, // 5 > 1
		{"", "0", "0"},  // записанный ноль лучше пустоты
		{"0", "±", "0"}, // равный ранг 0 → первая
		{"±", "", "±"},  // попытка лучше пустоты
	}
	for _, c := range cases {
		if got := manualCellMax(c.a, c.b); got != c.want {
			t.Errorf("manualCellMax(%q,%q)=%q want %q", c.a, c.b, got, c.want)
		}
	}
}

// Слияние таблиц разной ширины и с «грязными» именами (латинская ë, NBSP,
// лишние пробелы) — строки одного ученика склеиваются, не дублируются.
func TestMergeManualTablesMaxNormalizationAndWidth(t *testing.T) {
	// A: 2 колонки, имя с латинской ë; B: 3 колонки, то же имя кириллицей.
	a := "ФИО\t1\t2\nАртëм  Иванов\t1\t\n"      // латинская ë, двойной пробел
	b := "ФИО\t1\t2\t3\nАртём Иванов\t\t1\t+\n" // кириллическая ё
	got := MergeManualTablesMax(a, b)
	labels, rows := SplitManualTable(got, 0)
	if len(labels) != 3 {
		t.Fatalf("merged width must be 3: %v", labels)
	}
	if len(rows) != 1 {
		t.Fatalf("одинаковый ученик не должен дублироваться: %d строк\n%s", len(rows), got)
	}
	// max("1","")=1, max("","1")=1, max("(нет)","+")=+
	v := rows[0][1:]
	if v[0] != "1" || v[1] != "1" || v[2] != "+" {
		t.Fatalf("merged cells: %v", v)
	}
}

// NormalizeName едина: латинская ë, кириллическая ё, NBSP и регистр приводятся
// к одному виду.
func TestNormalizeName(t *testing.T) {
	if NormalizeName("Артём") != NormalizeName("артëм") {
		t.Fatal("ё и ë должны нормализоваться одинаково")
	}
	if NormalizeName("Иван Петров") != "иван петров" {
		t.Fatalf("NBSP: %q", NormalizeName("Иван Петров"))
	}
}
