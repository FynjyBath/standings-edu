package standings

import (
	"io"
	"log"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
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

// Момент заморозки считается при резолве от итогового окна.
func TestResolveGroupContestDefFreeze(t *testing.T) {
	start := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 19, 0, 0, 0, time.UTC)
	data := &domain.SourceData{Contests: map[string]domain.Contest{"c1": {ID: "c1"}}}

	freeze, _ := domain.ParseFreezeSpec("1h")
	got, ok := resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1", StartTime: &start, EndTime: &end, Freeze: freeze})
	if !ok || got.FreezeTime == nil || !got.FreezeTime.Equal(end.Add(-time.Hour)) {
		t.Fatalf("freeze time wrong: %+v", got.FreezeTime)
	}

	// Без окна заморозка не активируется.
	got, _ = resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1", Freeze: freeze})
	if got.FreezeTime != nil {
		t.Fatalf("freeze without window must be nil: %+v", got.FreezeTime)
	}

	// Без параметра — nil.
	got, _ = resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1", StartTime: &start, EndTime: &end})
	if got.FreezeTime != nil {
		t.Fatalf("no freeze param must be nil: %+v", got.FreezeTime)
	}
}

// Замороженная таблица: учитываются только посылки до момента заморозки,
// всё после (в т.ч. дорешка после конца) скрыто; FrozenAt проставлен.
func TestBuildTaskContestStandingsFrozen(t *testing.T) {
	start := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)              // 19:00
	freezeAt := end.Add(-time.Hour)              // 18:00
	solvedEarly := start.Add(30 * time.Minute)   // до заморозки — видно
	solvedLate := freezeAt.Add(10 * time.Minute) // после заморозки — скрыто
	upsolvedAfter := end.Add(time.Hour)          // дорешка — скрыта при заморозке

	contest := domain.Contest{
		ID: "c", Title: "К", ScoreSystem: domain.ScoreSystemEdu,
		StartTime: &start, EndTime: &end, FreezeTime: &freezeAt,
		Subcontests: []domain.Subcontest{{Title: "S", Tasks: []string{
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=1",
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=2",
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=3",
		}}},
	}
	urls := make([]string, 3)
	for i, raw := range contest.Subcontests[0].Tasks {
		urls[i] = domain.NormalizeTaskURL(raw)
	}

	st := newAccountStatuses()
	st.solved[urls[0]] = struct{}{}
	st.solved[urls[1]] = struct{}{}
	st.solved[urls[2]] = struct{}{}
	st.timed[urls[0]] = []source.TimedSubmission{{At: solvedEarly, Solved: true}}
	st.timed[urls[1]] = []source.TimedSubmission{{At: solvedLate, Solved: true}}
	st.timed[urls[2]] = []source.TimedSubmission{{At: upsolvedAfter, Solved: true}}

	b := NewBuilder(nil, log.New(io.Discard, "", 0), 1)
	students := []domain.Student{{ID: "s1", PublicName: "Ученик"}}
	out := b.buildTaskContestStandings(contest, students, map[string]*accountStatuses{"s1": st}, nil)

	if out.FrozenAt == nil || !out.FrozenAt.Equal(freezeAt) {
		t.Fatalf("FrozenAt not set: %+v", out.FrozenAt)
	}
	row := out.Rows[0]
	if row.Statuses[0] != domain.TaskStatusSolved {
		t.Fatalf("pre-freeze solve must show: %+v", row.Statuses)
	}
	if row.Statuses[1] != domain.TaskStatusNone || row.Statuses[2] != domain.TaskStatusNone {
		t.Fatalf("post-freeze results must be hidden: %+v", row.Statuses)
	}
	if row.SolvedCount != 1 || row.Upsolved != nil {
		t.Fatalf("frozen row wrong: solved=%d upsolved=%v", row.SolvedCount, row.Upsolved)
	}
	// Полная версия для токенного просмотра: все три решения (последнее — дорешка).
	if len(out.RowsFull) != 1 {
		t.Fatalf("frozen contest must carry rows_full: %+v", out.RowsFull)
	}
	fullRow := out.RowsFull[0]
	if fullRow.SolvedCount != 3 || fullRow.Upsolved == nil || !fullRow.Upsolved[2] {
		t.Fatalf("rows_full wrong: %+v", fullRow)
	}

	// Без заморозки: всё видно, поздняя задача после конца — дорешка.
	contest.FreezeTime = nil
	out = b.buildTaskContestStandings(contest, students, map[string]*accountStatuses{"s1": st}, nil)
	if out.FrozenAt != nil {
		t.Fatalf("FrozenAt must be nil without freeze: %+v", out.FrozenAt)
	}
	row = out.Rows[0]
	if row.SolvedCount != 3 {
		t.Fatalf("unfrozen must show all: %+v", row)
	}
	if row.Upsolved == nil || !row.Upsolved[2] || row.Upsolved[0] {
		t.Fatalf("upsolving mark wrong: %+v", row.Upsolved)
	}
	if out.RowsFull != nil {
		t.Fatalf("rows_full must be absent without freeze: %+v", out.RowsFull)
	}
}
