package standings

import (
	"testing"
	"time"

	"standings-edu/internal/domain"
)

// Окно контеста из записи группы переопределяет окно из определения контеста.
func TestResolveGroupContestDefWindowOverride(t *testing.T) {
	defStart := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	groupStart := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	groupEnd := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)

	data := &domain.SourceData{
		Contests: map[string]domain.Contest{
			"c1": {ID: "c1", Title: "К1", StartTime: &defStart},
		},
	}

	// Без переопределения — окно из определения.
	got, ok := resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1"})
	if !ok || got.StartTime == nil || !got.StartTime.Equal(defStart) || got.EndTime != nil {
		t.Fatalf("expected contest-level window, got %+v", got)
	}

	// Группа задаёт окно — оно приоритетнее.
	got, ok = resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1", StartTime: &groupStart, EndTime: &groupEnd})
	if !ok || got.StartTime == nil || !got.StartTime.Equal(groupStart) {
		t.Fatalf("group start must win: %+v", got.StartTime)
	}
	if got.EndTime == nil || !got.EndTime.Equal(groupEnd) {
		t.Fatalf("group end must win: %+v", got.EndTime)
	}

	// Переопределение работает и для inline-контеста.
	inline := domain.Contest{ID: "inl", Title: "Инлайн"}
	got, ok = resolveGroupContestDef(data, domain.GroupContestRef{ID: "inl", Inline: &inline, StartTime: &groupStart})
	if !ok || got.StartTime == nil || !got.StartTime.Equal(groupStart) {
		t.Fatalf("inline window override failed: %+v", got.StartTime)
	}
}
