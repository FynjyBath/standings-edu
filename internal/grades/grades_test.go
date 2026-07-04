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
