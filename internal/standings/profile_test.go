package standings

import (
	"io"
	"log"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

func testBuilderWithSites(t *testing.T) *Builder {
	t.Helper()
	reg := source.NewRegistry()
	reg.RegisterSite("acmp", source.NewACMPClient())
	reg.RegisterSite("codeforces", source.NewCodeforcesAPIClientWithState(""))
	inf, err := source.NewInformaticsAPIClientWithState(source.InformaticsCredentials{}, "")
	if err != nil {
		t.Fatalf("informatics client: %v", err)
	}
	reg.RegisterSite("informatics", inf)
	return NewBuilder(reg, log.New(io.Discard, "", 0), 1)
}

func TestBuildStudentProfile(t *testing.T) {
	b := testBuilderWithSites(t)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sc := func(v int) *int { return &v }

	norm := domain.NormalizeTaskURL
	cf1 := norm("https://codeforces.com/contest/1711/problem/A") // 2 попытки, решено
	cf2 := norm("https://codeforces.com/contest/1711/problem/B") // 1 попытка, решено
	cf5 := norm("https://codeforces.com/contest/1711/problem/C") // попытка, не решено
	inf3 := norm("https://informatics.msk.ru/mod/statements/view.php?chapterid=1791")
	acmp4 := norm("https://acmp.ru/?main=task&id_task=29") // решено, без времени

	st := newAccountStatuses()
	st.solved[cf1] = struct{}{}
	st.solved[cf2] = struct{}{}
	st.solved[inf3] = struct{}{}
	st.solved[acmp4] = struct{}{}
	for _, u := range []string{cf1, cf2, cf5, inf3, acmp4} {
		st.attempted[u] = struct{}{}
	}
	st.timed[cf1] = []source.TimedSubmission{
		{At: now.Add(-50 * time.Hour), Solved: false, Score: sc(0)},
		{At: now.Add(-49 * time.Hour), Solved: true, Score: sc(100)},
	}
	st.timed[cf2] = []source.TimedSubmission{{At: now.Add(-20 * time.Hour), Solved: true, Score: sc(100)}}
	st.timed[cf5] = []source.TimedSubmission{{At: now.Add(-2 * time.Hour), Solved: false, Score: sc(0)}}
	st.timed[inf3] = []source.TimedSubmission{{At: now.Add(-1 * time.Hour), Solved: true, Score: sc(100)}}

	student := domain.Student{ID: "s1", PublicName: "Ученик", Accounts: []domain.Account{{Site: "codeforces", AccountID: "u"}}}
	p := b.buildStudentProfile(student, st, now)

	if p.Stats.TotalSolved != 4 || p.Stats.TotalAttempted != 5 || p.Stats.TotalSubmissions != 5 {
		t.Fatalf("totals wrong: %+v", p.Stats)
	}
	if p.Stats.SolvedWithTimes != 3 || p.Stats.FirstTrySolved != 2 {
		t.Fatalf("speed counts wrong: %+v", p.Stats)
	}
	if p.Stats.AvgAttemptsToSolve != 1.3 { // (2+1+1)/3 = 1.33 -> 1.3
		t.Fatalf("avg attempts wrong: %v", p.Stats.AvgAttemptsToSolve)
	}
	// Окна активности: cf1 старше 30 дней? нет, 50 часов ~2 дня. Все 5 в пределах 7 дней.
	if p.Stats.Submissions7d != 5 || p.Stats.Submissions30d != 5 {
		t.Fatalf("activity windows wrong: %+v", p.Stats)
	}
	if p.Stats.LastActivity == nil || !p.Stats.LastActivity.Equal(now.Add(-1*time.Hour)) {
		t.Fatalf("last activity wrong: %v", p.Stats.LastActivity)
	}

	// Сайты.
	sites := map[string]domain.StudentSiteStat{}
	for _, s := range p.Sites {
		sites[s.Site] = s
	}
	if cf := sites["codeforces"]; cf.Solved != 2 || cf.Attempted != 3 || cf.Submissions != 4 || !cf.HasTimes {
		t.Fatalf("codeforces stat wrong: %+v", cf)
	}
	if inf := sites["informatics"]; inf.Solved != 1 || inf.Submissions != 1 || !inf.HasTimes {
		t.Fatalf("informatics stat wrong: %+v", inf)
	}
	if a := sites["acmp"]; a.Solved != 1 || a.Submissions != 0 || a.HasTimes {
		t.Fatalf("acmp stat wrong (must have no times): %+v", a)
	}

	// Лента: 5 посылок (acmp без времени не входит), отсортирована по убыванию.
	if len(p.Recent) != 5 {
		t.Fatalf("recent len: %d", len(p.Recent))
	}
	for i := 1; i < len(p.Recent); i++ {
		if p.Recent[i-1].At.Before(p.Recent[i].At) {
			t.Fatalf("recent not sorted desc at %d", i)
		}
	}
	if p.Recent[0].Label != "Инф 1791" || p.Recent[0].Site != "informatics" {
		t.Fatalf("newest submission wrong: %+v", p.Recent[0])
	}
	// Метка CF.
	foundCF := false
	for _, r := range p.Recent {
		if r.Label == "CF 1711A" {
			foundCF = true
		}
	}
	if !foundCF {
		t.Fatal("CF label not found")
	}

	// График: ровно 90 дней, последний = сегодня (MSK).
	if len(p.DailyActivity) != profileDailyDays {
		t.Fatalf("daily len: %d", len(p.DailyActivity))
	}
	todayMSK := now.In(moscowZone).Format("2006-01-02")
	if p.DailyActivity[len(p.DailyActivity)-1].Date != todayMSK {
		t.Fatalf("last day not today: %s vs %s", p.DailyActivity[len(p.DailyActivity)-1].Date, todayMSK)
	}
}

// addGroupPositions: место по доске почёта и по оценкам с учётом ничьих.
func TestAddGroupPositions(t *testing.T) {
	final := func(v float64) *float64 { return &v }
	std := domain.GeneratedGroupStandings{
		SolvedSummary: []domain.GeneratedGroupSolvedSummaryRow{
			{StudentID: "a", TotalSolvedCount: 10},
			{StudentID: "b", TotalSolvedCount: 10},
			{StudentID: "c", TotalSolvedCount: 3},
		},
		Grades: &domain.GeneratedGrades{Rows: []domain.GeneratedGradeRow{
			{StudentID: "a", Final: final(9)},
			{StudentID: "b", Final: final(5)},
			{StudentID: "c", Final: nil},
		}},
	}
	group := domain.GroupDefinition{Slug: "g1", Title: "Группа", StudentIDs: []string{"a", "b", "c"}}
	profiles := map[string]*domain.GeneratedStudentProfile{
		"a": {StudentID: "a"}, "b": {StudentID: "b"}, "c": {StudentID: "c"},
	}
	addGroupPositions(profiles, group, std)

	// a и b по 10 решённых — оба место 1 из 3; c — место 3.
	if profiles["a"].Groups[0].HonorPlace != 1 || profiles["b"].Groups[0].HonorPlace != 1 {
		t.Fatalf("tie honor place wrong: a=%+v b=%+v", profiles["a"].Groups[0], profiles["b"].Groups[0])
	}
	if profiles["c"].Groups[0].HonorPlace != 3 || profiles["c"].Groups[0].HonorTotal != 3 {
		t.Fatalf("c honor place wrong: %+v", profiles["c"].Groups[0])
	}
	// Оценки: a=9 место 1 из 2, b=5 место 2 из 2, c без оценки.
	if profiles["a"].Groups[0].GradePlace != 1 || profiles["a"].Groups[0].GradeTotal != 2 {
		t.Fatalf("a grade place wrong: %+v", profiles["a"].Groups[0])
	}
	if profiles["b"].Groups[0].GradePlace != 2 {
		t.Fatalf("b grade place wrong: %+v", profiles["b"].Groups[0])
	}
	if profiles["c"].Groups[0].Grade != nil || profiles["c"].Groups[0].GradeTotal != 0 {
		t.Fatalf("c must have no grade: %+v", profiles["c"].Groups[0])
	}
}

func TestTaskLabel(t *testing.T) {
	cases := []struct{ site, url, want string }{
		{"codeforces", "https://codeforces.com/problemset/problem/1711/A", "CF 1711A"},
		{"codeforces", "https://codeforces.com/contest/1711/problem/B", "CF 1711B"},
		{"informatics", "https://informatics.msk.ru/mod/statements/view.php?chapterid=1791", "Инф 1791"},
		{"acmp", "https://acmp.ru/?main=task&id_task=29", "ACMP 29"},
	}
	for _, c := range cases {
		if got := taskLabel(c.site, c.url); got != c.want {
			t.Fatalf("taskLabel(%s)=%q want %q", c.url, got, c.want)
		}
	}
}
