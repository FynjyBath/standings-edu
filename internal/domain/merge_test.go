package domain

import (
	"testing"
	"time"
)

func edu(id string, rows ...GeneratedRow) GeneratedContestStandings {
	return GeneratedContestStandings{ID: id, Title: id, ScoreSystem: ScoreSystemEdu, Rows: rows}
}

func row(sid string, name string, solved int) GeneratedRow {
	return GeneratedRow{StudentID: sid, PublicName: name, SolvedCount: solved, Statuses: []string{}}
}

// Контест с одинаковым id из двух групп сливается в одну таблицу с объединением
// учеников и пересчётом мест; контест только в одной группе остаётся отдельным.
func TestMergeGroupStandingsContests(t *testing.T) {
	a := GeneratedGroupStandings{
		GroupSlug: "a",
		Contests: []GeneratedContestStandings{
			edu("c1", row("s1", "Аня", 3), row("s2", "Боря", 1)),
			edu("c2", row("s1", "Аня", 2)), // только в группе A
		},
	}
	b := GeneratedGroupStandings{
		GroupSlug: "b",
		Contests: []GeneratedContestStandings{
			edu("c1", row("s3", "Вера", 2)),
			edu("c3", row("s3", "Вера", 5)), // только в группе B
		},
	}

	m := MergeGroupStandings("super", "Супер", []GeneratedGroupStandings{a, b})

	if m.GroupSlug != "super" || m.GroupTitle != "Супер" {
		t.Fatalf("meta wrong: %+v", m.GroupSlug)
	}
	// Порядок: c1 (первый в A), c2 (A), c3 (новый из B).
	ids := []string{m.Contests[0].ID, m.Contests[1].ID, m.Contests[2].ID}
	if len(m.Contests) != 3 || ids[0] != "c1" || ids[1] != "c2" || ids[2] != "c3" {
		t.Fatalf("contest order wrong: %v", ids)
	}

	// c1 объединил трёх учеников, пересчитал места: Аня(3) 1, Вера(2) 2, Боря(1) 3.
	c1 := m.Contests[0]
	if len(c1.Rows) != 3 {
		t.Fatalf("c1 must have 3 rows: %+v", c1.Rows)
	}
	want := []struct {
		sid, place string
	}{{"s1", "1"}, {"s3", "2"}, {"s2", "3"}}
	for i, w := range want {
		if c1.Rows[i].StudentID != w.sid || c1.Rows[i].Place != w.place {
			t.Fatalf("c1 row %d = (%s,%s), want (%s,%s)", i, c1.Rows[i].StudentID, c1.Rows[i].Place, w.sid, w.place)
		}
	}

	// c2 и c3 — по одному ученику, не трогались.
	if len(m.Contests[1].Rows) != 1 || m.Contests[1].Rows[0].StudentID != "s1" {
		t.Fatalf("c2 wrong: %+v", m.Contests[1].Rows)
	}
	if len(m.Contests[2].Rows) != 1 || m.Contests[2].Rows[0].StudentID != "s3" {
		t.Fatalf("c3 wrong: %+v", m.Contests[2].Rows)
	}
}

// Дубликат ученика (в двух группах) в общем контесте не задваивается.
func TestMergeDedupStudent(t *testing.T) {
	a := GeneratedGroupStandings{Contests: []GeneratedContestStandings{edu("c1", row("s1", "Аня", 3))}}
	b := GeneratedGroupStandings{Contests: []GeneratedContestStandings{edu("c1", row("s1", "Аня", 3), row("s2", "Боря", 1))}}
	m := MergeGroupStandings("s", "S", []GeneratedGroupStandings{a, b})
	if len(m.Contests[0].Rows) != 2 {
		t.Fatalf("student must be deduped: %+v", m.Contests[0].Rows)
	}
}

// Замороженный вклад: итог заморожен, rows_full собирается из полных строк
// (у незамороженного вклада полными считаются его обычные строки).
func TestMergeFrozenContest(t *testing.T) {
	frozenAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	aFrozen := GeneratedContestStandings{
		ID: "c1", ScoreSystem: ScoreSystemEdu, FrozenAt: &frozenAt,
		Rows:     []GeneratedRow{{StudentID: "s1", PublicName: "Аня", SolvedCount: 1}},
		RowsFull: []GeneratedRow{{StudentID: "s1", PublicName: "Аня", SolvedCount: 5}},
	}
	bOpen := edu("c1", row("s2", "Боря", 2))
	m := MergeGroupStandings("s", "S", []GeneratedGroupStandings{
		{Contests: []GeneratedContestStandings{aFrozen}},
		{Contests: []GeneratedContestStandings{bOpen}},
	})
	c := m.Contests[0]
	if c.FrozenAt == nil {
		t.Fatal("merged contest must be frozen")
	}
	if len(c.Rows) != 2 || len(c.RowsFull) != 2 {
		t.Fatalf("rows/rows_full must both have 2: %d/%d", len(c.Rows), len(c.RowsFull))
	}
	// В rows_full у s1 — полные 5 решённых, у s2 — его обычные 2.
	full := map[string]int{}
	for _, r := range c.RowsFull {
		full[r.StudentID] = r.SolvedCount
	}
	if full["s1"] != 5 || full["s2"] != 2 {
		t.Fatalf("rows_full wrong: %+v", full)
	}
}

// Сводка «решено» объединяется: сайты — объединение, ученик — один раз.
func TestMergeSolvedSummary(t *testing.T) {
	a := GeneratedGroupStandings{
		SolvedSummarySites: []string{"codeforces", "informatics"},
		SolvedSummary: []GeneratedGroupSolvedSummaryRow{
			{StudentID: "s1", PublicName: "Аня", TotalSolvedCount: 10, SolvedCountOnPageSites: 10, SolvedCountBySite: []int{7, 3}},
		},
	}
	b := GeneratedGroupStandings{
		SolvedSummarySites: []string{"codeforces", "informatics"},
		SolvedSummary: []GeneratedGroupSolvedSummaryRow{
			{StudentID: "s2", PublicName: "Боря", TotalSolvedCount: 20, SolvedCountOnPageSites: 20, SolvedCountBySite: []int{20, 0}},
		},
	}
	m := MergeGroupStandings("s", "S", []GeneratedGroupStandings{a, b})
	if len(m.SolvedSummarySites) != 2 {
		t.Fatalf("sites: %v", m.SolvedSummarySites)
	}
	if len(m.SolvedSummary) != 2 || m.SolvedSummary[0].StudentID != "s2" {
		// Боря (20) выше Ани (10).
		t.Fatalf("summary order/rows wrong: %+v", m.SolvedSummary)
	}
}
