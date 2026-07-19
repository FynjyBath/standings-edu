package grades

import (
	"testing"

	"standings-edu/internal/domain"
)

// Таблица оценок сортируется по убыванию итога; ученики без итога — внизу.
func TestBuildSortsByFinalDesc(t *testing.T) {
	cfg := &domain.GradesConfig{
		Columns: []domain.GradeColumn{
			{ID: "zachet", Title: "Зачет", Weight: 1, Type: domain.GradeColumnManual},
		},
	}
	roster := []RosterStudent{
		{ID: "a", PublicName: "Аня"},
		{ID: "b", PublicName: "Боря"},
		{ID: "c", PublicName: "Вера"},
		{ID: "d", PublicName: "Глеб"},
	}
	manual := map[string]map[string]float64{
		"zachet": {"a": 3, "c": 5, "d": 5},
	}

	got := Build(cfg, domain.GeneratedGroupStandings{}, roster, manual)
	if got == nil || len(got.Rows) != 4 {
		t.Fatalf("unexpected rows: %+v", got)
	}
	order := []string{got.Rows[0].StudentID, got.Rows[1].StudentID, got.Rows[2].StudentID, got.Rows[3].StudentID}
	// Вера и Глеб по 5 (при равенстве — по имени), Аня 3, Боря без оценки — внизу.
	want := []string{"c", "d", "a", "b"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("wrong order: got %v want %v", order, want)
		}
	}
	if got.Rows[3].Final != nil {
		t.Fatalf("student without grades must have nil final: %+v", got.Rows[3])
	}
}

func iptr(v int) *int         { return &v }
func fptr(v float64) *float64 { return &v }

// Final округляется до Round знаков, FinalRaw хранит то же среднее без округления.
func TestBuildFinalRawUnrounded(t *testing.T) {
	cfg := &domain.GradesConfig{
		Columns: []domain.GradeColumn{
			{ID: "x", Title: "X", Weight: 1, Type: domain.GradeColumnManual},
			{ID: "y", Title: "Y", Weight: 1, Type: domain.GradeColumnManual},
			{ID: "z", Title: "Z", Weight: 1, Type: domain.GradeColumnManual},
		},
	}
	roster := []RosterStudent{{ID: "a", PublicName: "Аня"}}
	manual := map[string]map[string]float64{"x": {"a": 10}, "y": {"a": 0}, "z": {"a": 0}}

	got := Build(cfg, domain.GeneratedGroupStandings{}, roster, manual)
	if got == nil || len(got.Rows) != 1 {
		t.Fatalf("unexpected: %+v", got)
	}
	row := got.Rows[0]
	// Среднее = 10/3 = 3.3333…; Round по умолчанию = 1 → Final 3.3.
	if row.Final == nil || *row.Final != 3.3 {
		t.Fatalf("Final must be rounded to 3.3: %v", row.Final)
	}
	if row.FinalRaw == nil || *row.FinalRaw < 3.3333 || *row.FinalRaw > 3.3334 {
		t.Fatalf("FinalRaw must be unrounded ~3.3333: %v", row.FinalRaw)
	}
	// Decimals — число знаков для единообразного показа всех оценок страницы.
	if got.Decimals == nil || *got.Decimals != 1 {
		t.Fatalf("Decimals must carry cfg round (default 1): %v", got.Decimals)
	}
	two := 2
	cfg.Round = &two
	if got2 := Build(cfg, domain.GeneratedGroupStandings{}, roster, manual); got2.Decimals == nil || *got2.Decimals != 2 {
		t.Fatalf("Decimals must follow cfg.Round=2: %v", got2.Decimals)
	}
	// round=0 округляет только итог: показ столбцов — 2 знака, не целые.
	zero := 0
	cfg.Round = &zero
	got3 := Build(cfg, domain.GeneratedGroupStandings{}, roster, manual)
	if got3.Decimals == nil || *got3.Decimals != 2 {
		t.Fatalf("при round=0 показ столбцов должен быть с 2 знаками: %v", got3.Decimals)
	}
	if f := got3.Rows[0].Final; f == nil || *f != 3 {
		t.Fatalf("итог при round=0 должен быть целым: %v", f)
	}
}

// Значения столбцов не огрубляются round-ом итога: реальное 10/3 остаётся
// ~3.3333 в столбце при любом round.
func TestBuildColumnValuesStayReal(t *testing.T) {
	zero := 0
	cfg := &domain.GradesConfig{
		Round:   &zero,
		Columns: []domain.GradeColumn{{ID: "x", Title: "X", Weight: 1, Type: domain.GradeColumnManual}},
	}
	roster := []RosterStudent{{ID: "a", PublicName: "Аня"}}
	manual := map[string]map[string]float64{"x": {"a": 10.0 / 3.0}}

	got := Build(cfg, domain.GeneratedGroupStandings{}, roster, manual)
	v := got.Rows[0].Values[0]
	if v == nil || *v < 3.3332 || *v > 3.3334 {
		t.Fatalf("значение столбца должно остаться реальным (~3.3333), а не огрубиться round-ом: %v", v)
	}
}

// Ранжирование — по точному итогу: при round=0 округлённые «4» и «4»
// упорядочиваются по FinalRaw, а не по имени.
func TestBuildSortsByFinalRaw(t *testing.T) {
	zero := 0
	cfg := &domain.GradesConfig{
		Round:   &zero,
		Columns: []domain.GradeColumn{{ID: "x", Title: "X", Weight: 1, Type: domain.GradeColumnManual}},
	}
	roster := []RosterStudent{{ID: "a", PublicName: "Аня"}, {ID: "b", PublicName: "Боря"}}
	manual := map[string]map[string]float64{"x": {"a": 3.6, "b": 4.4}}

	got := Build(cfg, domain.GeneratedGroupStandings{}, roster, manual)
	if got.Rows[0].StudentID != "b" || got.Rows[1].StudentID != "a" {
		t.Fatalf("сортировка должна идти по точному итогу (4.4 выше 3.6): %+v", got.Rows)
	}
	if *got.Rows[0].Final != 4 && *got.Rows[1].Final != 4 {
		t.Fatalf("оба итога округлены до 4: %+v", got.Rows)
	}
}

// Коэффициент дорешки: вклад задачи = max(основной, дорешка×k); штраф за нули
// контеста применяется и в оценке; plus-метрика даёт k за дорешанную задачу.
func TestBuildUpsolvingCoefficient(t *testing.T) {
	standings := domain.GeneratedGroupStandings{
		Contests: []domain.GeneratedContestStandings{{
			ID: "c1", TableNames: domain.TableNameList{"Тематические"}, ZeroPenalty: 5,
			Tasks: []domain.GeneratedTask{{Label: "A"}, {Label: "B"}, {Label: "C"}, {Label: "D"}},
			Rows: []domain.GeneratedRow{{
				StudentID: "a", PublicName: "Аня",
				// A: 50 в окне и 70 в дорешке; B: только дорешка 70;
				// C: 100 в окне; D: пусто (ноль со штрафом).
				Statuses:       []string{"solved", "solved", "solved", "none"},
				Scores:         []*int{iptr(50), nil, iptr(100), nil},
				PracticeScores: []*int{iptr(70), iptr(70), nil, nil},
				Upsolved:       []bool{false, true, false, false},
				TotalScore:     235, // старый путь (для сравнения)
				SolvedCount:    3,
			}},
		}},
	}
	roster := []RosterStudent{{ID: "a", PublicName: "Аня"}}

	// score + k=0.5: a + max(0,b−a)·k по задачам —
	//   A: 50 + (70−50)·0.5 = 60; B: 0 + 70·0.5 = 35; C: 100; D: 0 (штраф).
	//   Итого 60+35+100 − 5×1 = 190.
	cfg := &domain.GradesConfig{Columns: []domain.GradeColumn{{
		ID: "e", Title: "Т", Weight: 1, Type: "table", TableName: "Тематические",
		Metric: domain.GradeMetricScore, Upsolving: fptr(0.5),
	}}}
	got := Build(cfg, standings, roster, nil)
	// normalize max: единственный ученик — reference = его же 190 → 10 баллов.
	if got.Rows[0].Values[0] == nil || *got.Rows[0].Values[0] != 10 {
		t.Fatalf("score coef grade wrong: %+v", got.Rows[0].Values)
	}
	raw, ref, _ := computeTableColumn(cfg.Columns[0], standings, roster)
	if raw["a"] != 190 || ref["a"] != 190 {
		t.Fatalf("score coef raw/ref wrong: raw=%v ref=%v", raw["a"], ref["a"])
	}

	// k=0: дорешка не учитывается вовсе: 50 + 0 + 100 − 5×2 = 140 (B и D — нули).
	cfg.Columns[0].Upsolving = fptr(0)
	raw, _, _ = computeTableColumn(cfg.Columns[0], standings, roster)
	if raw["a"] != 140 {
		t.Fatalf("score coef=0 raw wrong: %v", raw["a"])
	}

	// plus + k=0.5: A решена в окне (1) + B дорешка (0.5) + C (1) + D (0) = 2.5.
	plusCol := domain.GradeColumn{ID: "p", Title: "П", Weight: 1, Type: "table",
		TableName: "Тематические", Metric: domain.GradeMetricPlus, Upsolving: fptr(0.5)}
	raw, _, _ = computeTableColumn(plusCol, standings, roster)
	if raw["a"] != 2.5 {
		t.Fatalf("plus coef raw wrong: %v", raw["a"])
	}

	// Без коэффициента — прежнее поведение (готовые суммы).
	oldCol := domain.GradeColumn{ID: "o", Title: "О", Weight: 1, Type: "table",
		TableName: "Тематические", Metric: domain.GradeMetricScore}
	raw, _, _ = computeTableColumn(oldCol, standings, roster)
	if raw["a"] != 235 {
		t.Fatalf("no-coef must use TotalScore: %v", raw["a"])
	}
}

// normalize max: сумма поконтестных максимумов, а не максимум общей суммы.
func TestNormalizeMaxPerContest(t *testing.T) {
	row := func(id string, total int) domain.GeneratedRow {
		return domain.GeneratedRow{StudentID: id, TotalScore: total}
	}
	standings := domain.GeneratedGroupStandings{
		Contests: []domain.GeneratedContestStandings{
			{ID: "c1", Rows: []domain.GeneratedRow{row("a", 100), row("b", 40)}},
			{ID: "c2", Rows: []domain.GeneratedRow{row("a", 20), row("b", 90)}},
		},
	}
	roster := []RosterStudent{{ID: "a", PublicName: "А"}, {ID: "b", PublicName: "Б"}}
	col := domain.GradeColumn{ID: "s", Title: "С", Weight: 1, Type: "table", Metric: domain.GradeMetricScore}

	raw, ref, _ := computeTableColumn(col, standings, roster)
	// Раньше reference был max(120, 130)=130; теперь 100+90=190.
	if ref["a"] != 190 || ref["b"] != 190 {
		t.Fatalf("reference must be sum of per-contest maxima: %v", ref)
	}
	if raw["a"] != 120 || raw["b"] != 130 {
		t.Fatalf("raw sums wrong: %v", raw)
	}
}

// IgnoreMissingContests: контесты без попыток и баллов не входят в знаменатель.
func TestIgnoreMissingContests(t *testing.T) {
	mk := func(id string, solved int, statuses ...string) domain.GeneratedRow {
		return domain.GeneratedRow{StudentID: id, SolvedCount: solved, Statuses: statuses}
	}
	tasks := []domain.GeneratedTask{{Label: "A"}, {Label: "B"}}
	standings := domain.GeneratedGroupStandings{
		Contests: []domain.GeneratedContestStandings{
			{ID: "c1", Tasks: tasks, Rows: []domain.GeneratedRow{
				mk("a", 2, "solved", "solved"),
				mk("b", 1, "solved", "none"),
				mk("c", 0, "none", "none"), // Вера пропустила всё
			}},
			{ID: "c2", Tasks: tasks, Rows: []domain.GeneratedRow{
				mk("a", 1, "solved", "none"),
				mk("b", 0, "none", "none"), // Боря пропустил второй контест целиком
				mk("c", 0, "none", "none"),
			}},
		},
	}
	roster := []RosterStudent{{ID: "a", PublicName: "Аня"}, {ID: "b", PublicName: "Боря"}, {ID: "c", PublicName: "Вера"}}

	build := func(ignore bool) *domain.GeneratedGrades {
		return Build(&domain.GradesConfig{Round: iptr(2), Columns: []domain.GradeColumn{{
			ID: "t", Title: "Т", Weight: 1, Type: "table", Metric: domain.GradeMetricPlus,
			Normalize: domain.NormalizeSpec{Mode: domain.NormalizeTotal}, IgnoreMissingContests: ignore,
		}}}, standings, roster, nil)
	}
	valueOf := func(g *domain.GeneratedGrades, id string) *float64 {
		for _, r := range g.Rows {
			if r.StudentID == id {
				return r.Values[0]
			}
		}
		t.Fatalf("student %s not found", id)
		return nil
	}
	eq := func(p *float64, want float64) bool { return p != nil && *p == want }

	// Обычный режим: знаменатель = все 4 задачи. A:3/4·10=7.5, B:1/4·10=2.5, C:0.
	normal := build(false)
	if !eq(valueOf(normal, "a"), 7.5) || !eq(valueOf(normal, "b"), 2.5) || !eq(valueOf(normal, "c"), 0) {
		t.Fatalf("normal mode wrong: a=%v b=%v c=%v", valueOf(normal, "a"), valueOf(normal, "b"), valueOf(normal, "c"))
	}

	// Режим «не учитывать пропущенные»:
	//   A участвовал в обоих → 3/4·10 = 7.5 (без изменений);
	//   B пропустил c2 → знаменатель только 2 задачи c1 → 1/2·10 = 5.0;
	//   C пропустил всё → оценки по столбцу нет (nil, и итог nil).
	ign := build(true)
	if !eq(valueOf(ign, "a"), 7.5) {
		t.Fatalf("ignore: a должен остаться 7.5: %v", valueOf(ign, "a"))
	}
	if !eq(valueOf(ign, "b"), 5.0) {
		t.Fatalf("ignore: b должен вырасти до 5.0: %v", valueOf(ign, "b"))
	}
	if valueOf(ign, "c") != nil {
		t.Fatalf("ignore: у полностью пропустившего оценки быть не должно: %v", valueOf(ign, "c"))
	}
	for _, r := range ign.Rows {
		if r.StudentID == "c" && r.Final != nil {
			t.Fatalf("ignore: итог у Веры должен быть nil: %v", *r.Final)
		}
	}
}
