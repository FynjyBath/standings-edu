package standings

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// zeroScoreSite — сайт вроде ejudge в ACM-режиме: баллов нет, и score=0 приходит
// и у решения, и у неудачной попытки.
type zeroScoreSite struct{ results []source.TaskResult }

func (s zeroScoreSite) FetchUserResults(context.Context, string) ([]source.TaskResult, error) {
	return s.results, nil
}
func (zeroScoreSite) SupportsTaskScores() bool { return true }
func (zeroScoreSite) MatchTaskURL(u string) bool {
	_, ok := domain.ParseEjudgeTaskURL(u)
	return ok
}

func intp(v int) *int { return &v }

// Решённая задача с нулём баллов — это контест без баллов, а не «решено на 0»:
// она стоит полный балл и в таблице с баллами, и в ленте профиля. Частичный
// балл решения и ноль у попытки остаются как есть.
func TestSolvedZeroScoreMeansFullScore(t *testing.T) {
	const (
		acmTask     = "https://ej.example.ru/new-client?contest_id=1&prob_id=1"
		failedTask  = "https://ej.example.ru/new-client?contest_id=1&prob_id=2"
		partialTask = "https://ej.example.ru/new-client?contest_id=1&prob_id=3"
	)
	at := func(m int) time.Time { return time.Date(2026, 10, 1, 10, m, 0, 0, time.UTC) }
	acmTimed := []source.TimedSubmission{
		{At: at(0), Solved: false, Score: intp(0)},
		{At: at(5), Solved: true, Score: intp(0)},
	}
	site := zeroScoreSite{results: []source.TaskResult{
		{TaskURL: acmTask, Attempted: true, Solved: true, Score: intp(0), Timed: acmTimed},
		{TaskURL: failedTask, Attempted: true, Score: intp(0),
			Timed: []source.TimedSubmission{{At: at(10), Score: intp(0)}}},
		{TaskURL: partialTask, Attempted: true, Solved: true, Score: intp(60),
			Timed: []source.TimedSubmission{{At: at(15), Solved: true, Score: intp(60)}}},
	}}
	reg := source.NewRegistry()
	reg.RegisterSite("ej", site)
	b := NewBuilder(reg, log.New(io.Discard, "", 0), 1)

	st, err := b.fetchAccountStatuses(context.Background(), "ej", "login")
	if err != nil {
		t.Fatal(err)
	}
	acm, failed, partial := domain.NormalizeTaskURL(acmTask), domain.NormalizeTaskURL(failedTask), domain.NormalizeTaskURL(partialTask)
	if got := st.scores[acm]; got != 100 {
		t.Errorf("решённая задача без баллов: балл %d, want 100", got)
	}
	if got := st.scores[failed]; got != 0 {
		t.Errorf("нерешённая задача: балл %d, want 0", got)
	}
	if got := st.scores[partial]; got != 60 {
		t.Errorf("частичный балл решения: %d, want 60", got)
	}
	if subs := st.timed[acm]; len(subs) != 2 || *subs[0].Score != 0 || *subs[1].Score != 100 {
		t.Errorf("посылки: попытка должна остаться 0, решение — стать 100: %+v", subs)
	}
	// Кэш клиента сайта не тронут: баллы под его указателями прежние.
	if *acmTimed[1].Score != 0 {
		t.Error("исправление не должно менять данные клиента сайта")
	}

	// Таблица с баллами: решённая задача даёт полный балл в сумму.
	statuses := map[string]*accountStatuses{"s1": newAccountStatuses()}
	mergeStatuses(statuses["s1"], st)
	contest := domain.Contest{
		ID: "c", ScoreSystem: domain.ScoreSystemIOI,
		Subcontests: []domain.Subcontest{{Title: "S", Tasks: []string{acmTask, failedTask, partialTask}}},
	}
	students := []domain.Student{{ID: "s1", PublicName: "У"}}
	for name, c := range map[string]domain.Contest{"без окна": contest, "с окном": withWindow(contest, at(0), at(30))} {
		std := b.buildTaskContestStandings(c, students, statuses, nil, nil, nil, nil, nil)
		if got := std.Rows[0].TotalScore; got != 160 {
			t.Errorf("%s: сумма %d, want 160 (100 за решение без баллов + 0 + 60)", name, got)
		}
	}

	// Лента профиля: у решения полный балл, у попытки ноль.
	p := b.buildStudentProfiles(students, statuses, nil, at(60))["s1"]
	for _, sub := range p.Recent {
		if sub.TaskURL != acm {
			continue
		}
		want := 0
		if sub.Solved {
			want = 100
		}
		if sub.Score == nil || *sub.Score != want {
			t.Errorf("лента: solved=%v score=%v, want %d", sub.Solved, sub.Score, want)
		}
	}
}

func withWindow(c domain.Contest, start, end time.Time) domain.Contest {
	c.StartTime, c.EndTime = &start, &end
	return c
}
