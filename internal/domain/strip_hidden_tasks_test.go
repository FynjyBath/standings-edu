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

// StripTaskLinks зануляет ссылки на задачи (в заголовках и плоском списке) и
// source_url, оставляя метки; на строки/статусы не влияет.
func TestStripTaskLinks(t *testing.T) {
	std := GeneratedGroupStandings{
		Contests: []GeneratedContestStandings{{
			ID:        "c1",
			SourceURL: "https://informatics.msk.ru/x?id=5",
			Tasks: []GeneratedTask{
				{Label: "A", URL: "u1", NormalizedURL: "n1", Name: "Задача A"},
				{Label: "B", URL: "u2", NormalizedURL: "n2", Name: "Задача B"},
			},
			Subcontests: []GeneratedSubcontest{{
				Title: "S", TaskCount: 2,
				Tasks: []GeneratedTask{
					{Label: "A", URL: "u1", NormalizedURL: "n1", Name: "Задача A"},
					{Label: "B", URL: "u2", NormalizedURL: "n2", Name: "Задача B"},
				},
			}},
			Rows: []GeneratedRow{{StudentID: "s1", Statuses: []string{"solved", "none"}}},
		}},
	}
	std.StripTaskLinks()
	c := std.Contests[0]
	for i, tk := range c.Tasks {
		if tk.URL != "" || tk.NormalizedURL != "" || tk.Name != "" || tk.Label == "" {
			t.Fatalf("task %d: url и название должны быть пусты, метка сохранена: %+v", i, tk)
		}
	}
	for _, tk := range c.Subcontests[0].Tasks {
		if tk.URL != "" || tk.Name != "" {
			t.Fatalf("subcontest task url/название должны быть пусты: %+v", tk)
		}
	}
	if c.SourceURL != "" {
		t.Fatalf("source_url должен быть пуст: %q", c.SourceURL)
	}
	if len(c.Rows) != 1 || c.Rows[0].StudentID != "s1" {
		t.Fatal("строки не должны меняться")
	}
}
