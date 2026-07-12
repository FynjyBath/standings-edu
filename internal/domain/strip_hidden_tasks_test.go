package domain

import "testing"

func sp(v int) *int { return &v }

// StripHiddenTasks вырезает скрытые колонки, перенумеровывает оставшиеся,
// фильтрует массивы строк и уменьшает счётчики решённых/сумму баллов.
func TestStripHiddenTasks(t *testing.T) {
	std := GeneratedGroupStandings{
		Contests: []GeneratedContestStandings{{
			ID:          "c1",
			ScoreSystem: ScoreSystemIOI,
			Subcontests: []GeneratedSubcontest{{
				Title:     "S",
				TaskCount: 3,
				Tasks: []GeneratedTask{
					{Label: "A", URL: "u1"},
					{Label: "B", URL: "u2", Hidden: true},
					{Label: "C", URL: "u3"},
				},
			}},
			Tasks: []GeneratedTask{
				{Label: "A", URL: "u1"},
				{Label: "B", URL: "u2", Hidden: true},
				{Label: "C", URL: "u3"},
			},
			Rows: []GeneratedRow{{
				StudentID:   "s1",
				SolvedCount: 3,
				TotalScore:  250,
				Statuses:    []string{TaskStatusSolved, TaskStatusSolved, TaskStatusSolved},
				Scores:      []*int{sp(100), sp(80), sp(70)},
			}},
		}},
	}

	std.StripHiddenTasks()

	c := std.Contests[0]
	if len(c.Tasks) != 2 || c.Tasks[0].Label != "A" || c.Tasks[1].Label != "B" {
		t.Fatalf("tasks after strip: %+v", c.Tasks)
	}
	if c.Tasks[1].URL != "u3" {
		t.Fatalf("second visible task must be u3 (relabeled B): %+v", c.Tasks[1])
	}
	if c.Subcontests[0].TaskCount != 2 || len(c.Subcontests[0].Tasks) != 2 {
		t.Fatalf("subcontest count: %+v", c.Subcontests[0])
	}
	r := c.Rows[0]
	if len(r.Statuses) != 2 || len(r.Scores) != 2 {
		t.Fatalf("row arrays not filtered: %+v", r)
	}
	if r.Scores[0] == nil || *r.Scores[0] != 100 || r.Scores[1] == nil || *r.Scores[1] != 70 {
		t.Fatalf("row scores wrong: %v %v", r.Scores[0], r.Scores[1])
	}
	// Скрытая B решена (80 баллов) → SolvedCount 3-1=2, TotalScore 250-80=170.
	if r.SolvedCount != 2 || r.TotalScore != 170 {
		t.Fatalf("aggregates wrong: solved=%d total=%d", r.SolvedCount, r.TotalScore)
	}
}

// Без скрытых задач StripHiddenTasks не трогает строки (тот же срез).
func TestStripHiddenTasksNoop(t *testing.T) {
	rows := []GeneratedRow{{StudentID: "s1", Statuses: []string{TaskStatusSolved}}}
	std := GeneratedGroupStandings{
		Contests: []GeneratedContestStandings{{
			ID:          "c1",
			Tasks:       []GeneratedTask{{Label: "A", URL: "u1"}},
			Subcontests: []GeneratedSubcontest{{Tasks: []GeneratedTask{{Label: "A", URL: "u1"}}, TaskCount: 1}},
			Rows:        rows,
		}},
	}
	std.StripHiddenTasks()
	if &std.Contests[0].Rows[0] != &rows[0] {
		t.Fatal("rows must be shared (not reallocated) when nothing hidden")
	}
}
