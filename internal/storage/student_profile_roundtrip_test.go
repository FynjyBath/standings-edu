package storage

import (
	"testing"
	"time"

	"standings-edu/internal/domain"
)

// Новые поля обязаны пережить запись и чтение. Прошлый раз поле молча терялось
// на белом списке в UnmarshalJSON, и нашлось это только на живом сервере.
func TestStudentCourseStatsRoundTripKeepsNewFields(t *testing.T) {
	dir := t.TempDir()
	w := NewGeneratedWriter(dir)
	want := domain.GeneratedStudentProfile{
		StudentID: "s1", PublicName: "Иванов И.",
		CourseStats: []domain.StudentCourseStats{{
			GroupSlug: "g", GroupTitle: "Г",
			Progress: 0.5, SolvedCount: 10, TotalCount: 20,
			Tempo: 1.25, TempoRecent: 1.4,
			Mind: 0.62, Accuracy: 1.35,
			JudgeHours: 3.2,
			Global: &domain.StudentCourseStats{
				GroupSlug: "g", Mind: 0.58, Accuracy: 1.1, Tempo: 1.0,
			},
		}},
		GeneratedAt: ptr(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)),
	}
	if err := w.WriteStudentProfile(want); err != nil {
		t.Fatal(err)
	}
	got, err := NewGeneratedLoader(dir).LoadStudentProfile("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CourseStats) != 1 {
		t.Fatalf("курсов %d", len(got.CourseStats))
	}
	g := got.CourseStats[0]
	if g.Mind != 0.62 {
		t.Errorf("Mind потерялся: %v", g.Mind)
	}
	if g.Accuracy != 1.35 {
		t.Errorf("Accuracy потерялся: %v", g.Accuracy)
	}
	if g.Tempo != 1.25 || g.TempoRecent != 1.4 {
		t.Errorf("темп потерялся: %+v", g)
	}
	if g.Global == nil || g.Global.Mind != 0.58 || g.Global.Accuracy != 1.1 {
		t.Errorf("вложенный Global потерял поля: %+v", g.Global)
	}
}

func ptr(t time.Time) *time.Time { return &t }
