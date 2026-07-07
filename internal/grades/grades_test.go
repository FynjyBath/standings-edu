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
	raw, ref := computeTableColumn(cfg.Columns[0], standings, roster)
	if raw["a"] != 190 || ref != 190 {
		t.Fatalf("score coef raw/ref wrong: raw=%v ref=%v", raw["a"], ref)
	}

	// k=0: дорешка не учитывается вовсе: 50 + 0 + 100 − 5×2 = 140 (B и D — нули).
	cfg.Columns[0].Upsolving = fptr(0)
	raw, _ = computeTableColumn(cfg.Columns[0], standings, roster)
	if raw["a"] != 140 {
		t.Fatalf("score coef=0 raw wrong: %v", raw["a"])
	}

	// plus + k=0.5: A решена в окне (1) + B дорешка (0.5) + C (1) + D (0) = 2.5.
	plusCol := domain.GradeColumn{ID: "p", Title: "П", Weight: 1, Type: "table",
		TableName: "Тематические", Metric: domain.GradeMetricPlus, Upsolving: fptr(0.5)}
	raw, _ = computeTableColumn(plusCol, standings, roster)
	if raw["a"] != 2.5 {
		t.Fatalf("plus coef raw wrong: %v", raw["a"])
	}

	// Без коэффициента — прежнее поведение (готовые суммы).
	oldCol := domain.GradeColumn{ID: "o", Title: "О", Weight: 1, Type: "table",
		TableName: "Тематические", Metric: domain.GradeMetricScore}
	raw, _ = computeTableColumn(oldCol, standings, roster)
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

	raw, ref := computeTableColumn(col, standings, roster)
	// Раньше reference был max(120, 130)=130; теперь 100+90=190.
	if ref != 190 {
		t.Fatalf("reference must be sum of per-contest maxima: %v", ref)
	}
	if raw["a"] != 120 || raw["b"] != 130 {
		t.Fatalf("raw sums wrong: %v", raw)
	}
}
