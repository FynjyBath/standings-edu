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
	out := b.buildTaskContestStandings(contest, students, map[string]*accountStatuses{"s1": st}, nil, nil, nil)

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
	out = b.buildTaskContestStandings(contest, students, map[string]*accountStatuses{"s1": st}, nil, nil, nil)
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

// Штраф за нули и раздельные баллы основного времени/дорешки (ioi).
func TestBuildTaskContestStandingsZeroPenaltyAndPracticeScores(t *testing.T) {
	start := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	inTime := start.Add(30 * time.Minute)
	afterEnd := end.Add(time.Hour)

	score := func(v int) *int { return &v }
	contest := domain.Contest{
		ID: "c", Title: "К", ScoreSystem: domain.ScoreSystemIOI, ZeroPenalty: 5,
		SummaryTotalOnly: true,
		StartTime:        &start, EndTime: &end,
		Subcontests: []domain.Subcontest{{Title: "S", Tasks: []string{
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=1", // 100 в окне
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=2", // только дорешка 70
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=3", // 50 в окне + 70 в дорешке
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=4", // не сдавал — ноль со штрафом
		}}},
	}
	urls := make([]string, 4)
	for i, raw := range contest.Subcontests[0].Tasks {
		urls[i] = domain.NormalizeTaskURL(raw)
	}

	st := newAccountStatuses()
	st.solved[urls[0]] = struct{}{}
	st.solved[urls[1]] = struct{}{}
	st.solved[urls[2]] = struct{}{}
	st.timed[urls[0]] = []source.TimedSubmission{{At: inTime, Solved: true, Score: score(100)}}
	st.timed[urls[1]] = []source.TimedSubmission{{At: afterEnd, Solved: true, Score: score(70)}}
	st.timed[urls[2]] = []source.TimedSubmission{
		{At: inTime, Solved: false, Score: score(50)},
		{At: afterEnd, Solved: true, Score: score(70)},
	}

	b := NewBuilder(nil, log.New(io.Discard, "", 0), 1)
	out := b.buildTaskContestStandings(contest, []domain.Student{{ID: "s1", PublicName: "У"}}, map[string]*accountStatuses{"s1": st}, nil, nil, nil)

	if out.ZeroPenalty != 5 {
		t.Fatalf("contest zero_penalty not stored: %+v", out.ZeroPenalty)
	}
	if !out.SummaryTotalOnly {
		t.Fatal("summary_total_only not carried into generated contest")
	}
	row := out.Rows[0]
	wantMain := []*int{score(100), nil, score(50), nil}
	wantPractice := []*int{nil, score(70), score(70), nil}
	for i := range wantMain {
		if !intPtrEq(row.Scores[i], wantMain[i]) {
			t.Fatalf("main score[%d] mismatch: %+v", i, row.Scores)
		}
		var got *int
		if i < len(row.PracticeScores) {
			got = row.PracticeScores[i]
		}
		if !intPtrEq(got, wantPractice[i]) {
			t.Fatalf("practice score[%d] mismatch: %+v", i, row.PracticeScores)
		}
	}
	// Сумма: 100 + 70 + max(50,70) − 5×1 (одна пустая задача) = 235.
	if row.TotalScore != 235 {
		t.Fatalf("total with penalty wrong: %d", row.TotalScore)
	}
	if row.Penalty == nil || *row.Penalty != 5 {
		t.Fatalf("penalty column wrong: %+v", row.Penalty)
	}

	// edu-контест: штраф игнорируется, Penalty пуст.
	eduContest := contest
	eduContest.ScoreSystem = domain.ScoreSystemEdu
	out = b.buildTaskContestStandings(eduContest, []domain.Student{{ID: "s1", PublicName: "У"}}, map[string]*accountStatuses{"s1": st}, nil, nil, nil)
	if out.ZeroPenalty != 0 || out.Rows[0].Penalty != nil {
		t.Fatalf("edu must ignore zero_penalty: %+v", out.Rows[0].Penalty)
	}
}

func intPtrEq(a, b *int) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

// Наследование параметров: значение из записи группы переопределяет
// определение контеста; отсутствие — наследует; спецзначения выключают.
func TestResolveGroupContestDefInheritance(t *testing.T) {
	start := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 19, 0, 0, 0, time.UTC)
	data := &domain.SourceData{Contests: map[string]domain.Contest{
		"c1": {ID: "c1", StartTime: &start, EndTime: &end,
			Freeze: "1h", ZeroPenalty: 5, SummaryTotalOnly: true, Hidden: true,
			TableNames: domain.TableNameList{"Глоб"}},
	}}

	// Без переопределений — всё из определения (глобальная заморозка активна).
	got, _ := resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1"})
	if got.FreezeTime == nil || !got.FreezeTime.Equal(end.Add(-time.Hour)) {
		t.Fatalf("global freeze must apply: %+v", got.FreezeTime)
	}
	if got.ZeroPenalty != 5 || !got.SummaryTotalOnly || !got.Hidden || got.TableNames[0] != "Глоб" {
		t.Fatalf("global params must apply: %+v", got)
	}

	// Локальные переопределения побеждают; freeze "none" выключает глобальную.
	zp := 0
	sum := false
	hid := false
	freezeNone, _ := domain.ParseFreezeSpec("none")
	got, _ = resolveGroupContestDef(data, domain.GroupContestRef{
		ID: "c1", Freeze: freezeNone, ZeroPenalty: &zp, SummaryTotalOnly: &sum, Hidden: &hid,
	})
	if got.FreezeTime != nil {
		t.Fatalf("freeze none must disable global: %+v", got.FreezeTime)
	}
	if got.ZeroPenalty != 0 || got.SummaryTotalOnly || got.Hidden {
		t.Fatalf("local disable must win: %+v", got)
	}

	// Локальная заморозка с другой длительностью.
	freeze30, _ := domain.ParseFreezeSpec("30m")
	got, _ = resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1", Freeze: freeze30})
	if got.FreezeTime == nil || !got.FreezeTime.Equal(end.Add(-30*time.Minute)) {
		t.Fatalf("local freeze must override: %+v", got.FreezeTime)
	}

	// Локальный штраф больше глобального.
	zp9 := 9
	got, _ = resolveGroupContestDef(data, domain.GroupContestRef{ID: "c1", ZeroPenalty: &zp9})
	if got.ZeroPenalty != 9 {
		t.Fatalf("local penalty must override: %+v", got.ZeroPenalty)
	}
}

// «Зачтено» доходит до GeneratedRow.Accepted: без окна — по okSolved,
// с окном — по TimedSubmission.Accepted.
func TestBuildTaskContestAcceptedMark(t *testing.T) {
	contest := domain.Contest{
		ID: "c", ScoreSystem: domain.ScoreSystemEdu,
		Subcontests: []domain.Subcontest{{Title: "S", Tasks: []string{
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=1", // зачтено
			"https://informatics.msk.ru/mod/statements/view.php?chapterid=2", // полный OK
		}}},
	}
	u := make([]string, 2)
	for i, raw := range contest.Subcontests[0].Tasks {
		u[i] = domain.NormalizeTaskURL(raw)
	}
	b := NewBuilder(nil, log.New(io.Discard, "", 0), 1)
	students := []domain.Student{{ID: "s1", PublicName: "У"}}

	// Без окна: solved обе, okSolved только у второй.
	st := newAccountStatuses()
	st.solved[u[0]] = struct{}{}
	st.solved[u[1]] = struct{}{}
	st.okSolved[u[1]] = struct{}{}
	out := b.buildTaskContestStandings(contest, students, map[string]*accountStatuses{"s1": st}, nil, nil, nil)
	row := out.Rows[0]
	if row.Accepted == nil || !row.Accepted[0] || row.Accepted[1] {
		t.Fatalf("no-window accepted marks wrong: %+v", row.Accepted)
	}

	// С окном: обе решены в окне, первая — «зачтено» (Accepted), вторая — OK.
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	inTime := start.Add(30 * time.Minute)
	contest.StartTime, contest.EndTime = &start, &end
	st2 := newAccountStatuses()
	st2.timed[u[0]] = []source.TimedSubmission{{At: inTime, Solved: true, Accepted: true}}
	st2.timed[u[1]] = []source.TimedSubmission{{At: inTime, Solved: true, Accepted: false}}
	out = b.buildTaskContestStandings(contest, students, map[string]*accountStatuses{"s1": st2}, nil, nil, nil)
	row = out.Rows[0]
	if row.Accepted == nil || !row.Accepted[0] || row.Accepted[1] {
		t.Fatalf("windowed accepted marks wrong: %+v", row.Accepted)
	}
}

// singleTaskLink: одна ссылка на все подконтесты → она и возвращается; несколько
// или ноль — ok=false.
func TestSingleTaskLink(t *testing.T) {
	one := domain.Contest{Subcontests: []domain.Subcontest{
		{Tasks: []string{"https://informatics.msk.ru/mod/statements/view.php?id=52798"}},
	}}
	if link, ok := singleTaskLink(one); !ok || link != "https://informatics.msk.ru/mod/statements/view.php?id=52798" {
		t.Fatalf("одна ссылка: ok=%v link=%q", ok, link)
	}
	many := domain.Contest{Subcontests: []domain.Subcontest{
		{Tasks: []string{"a", "b"}},
	}}
	if _, ok := singleTaskLink(many); ok {
		t.Fatal("несколько ссылок → ok должно быть false")
	}
	none := domain.Contest{Subcontests: []domain.Subcontest{{Tasks: []string{"  "}}}}
	if _, ok := singleTaskLink(none); ok {
		t.Fatal("пустые ссылки → ok должно быть false")
	}
}

// Рамка в окне контеста провайдер-специфична. informatics (suppressBorder=true):
// «зачтено» с рамкой, полный OK перебивает рамку. ejudge (suppressBorder=false):
// OK с рамкой, «ожидает подтверждения» без; OK+ожидает → рамка остаётся.
func TestWindowedTaskResultBorder(t *testing.T) {
	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	at := start.Add(30 * time.Minute)
	sub := func(border bool) source.TimedSubmission {
		return source.TimedSubmission{At: at, Solved: true, Accepted: border}
	}
	border := func(subs []source.TimedSubmission, suppress bool) bool {
		_, _, _, _, acc, _ := windowedTaskResult(subs, start, end, false, false, suppress)
		return acc
	}

	// informatics: Accepted=зачтено(8), OK=неAccepted.
	if !border([]source.TimedSubmission{sub(true)}, true) {
		t.Fatal("informatics: зачтено → рамка")
	}
	if border([]source.TimedSubmission{sub(false)}, true) {
		t.Fatal("informatics: OK → без рамки")
	}
	if border([]source.TimedSubmission{sub(true), sub(false)}, true) {
		t.Fatal("informatics: зачтено+OK → OK перебивает, без рамки")
	}

	// ejudge: Accepted=OK(0), ожидает=неAccepted; ничего не перебивает OK.
	if !border([]source.TimedSubmission{sub(true)}, false) {
		t.Fatal("ejudge: OK → рамка")
	}
	if border([]source.TimedSubmission{sub(false)}, false) {
		t.Fatal("ejudge: ожидает подтверждения → без рамки")
	}
	if !border([]source.TimedSubmission{sub(true), sub(false)}, false) {
		t.Fatal("ejudge: OK+ожидает → рамка остаётся")
	}
}
