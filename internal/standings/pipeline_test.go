package standings

import (
	"io"
	"log"
	"testing"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
)

func testPipeline(t *testing.T) *Pipeline {
	t.Helper()
	out := t.TempDir()
	writer := storage.NewGeneratedWriter(out)
	return &Pipeline{
		writer:          writer,
		generatedLoader: storage.NewGeneratedLoader(out),
		logger:          log.New(io.Discard, "", 0),
	}
}

func upd(v bool) domain.GroupContestRef {
	return domain.GroupContestRef{Update: v}
}

// Группа с update=false контестом: доска почёта берётся из свежей сборки
// (регрессия: раньше подменялась старой, и новые ученики не появлялись),
// а состав строк замороженного контеста согласуется с текущим ростером.
func TestMergeNonUpdatedContestsFreshSummaryAndRoster(t *testing.T) {
	p := testPipeline(t)

	oldStandings := domain.GeneratedGroupStandings{
		GroupSlug:          "grp",
		GroupTitle:         "Группа",
		SolvedSummarySites: []string{"acmp"},
		SolvedSummary: []domain.GeneratedGroupSolvedSummaryRow{
			{StudentID: "old", PublicName: "Старый", SolvedCountBySite: []int{5}},
		},
		Contests: []domain.GeneratedContestStandings{
			{
				ID:    "frozen",
				Tasks: []domain.GeneratedTask{{Label: "A"}, {Label: "B"}},
				Rows: []domain.GeneratedRow{
					{StudentID: "old", PublicName: "Старое Имя", SolvedCount: 2, Statuses: []string{"solved", "solved"}},
					{StudentID: "gone", PublicName: "Выбывший", SolvedCount: 1, Statuses: []string{"solved", "none"}},
				},
			},
		},
	}
	if err := p.writer.WriteGroupStandings(oldStandings); err != nil {
		t.Fatal(err)
	}

	group := domain.GroupDefinition{
		Slug:       "grp",
		Title:      "Группа",
		StudentIDs: []string{"old", "new"},
		Contests: []domain.GroupContestRef{
			{ID: "built", Update: true},
			{ID: "frozen", Update: false},
		},
	}
	students := map[string]domain.Student{
		"old": {ID: "old", PublicName: "Новое Имя"},
		"new": {ID: "new", PublicName: "Новичок"},
	}
	freshBuild := domain.GeneratedGroupStandings{
		GroupSlug:          "grp",
		SolvedSummarySites: []string{"acmp", "informatics"},
		SolvedSummary: []domain.GeneratedGroupSolvedSummaryRow{
			{StudentID: "old", PublicName: "Новое Имя", SolvedCountBySite: []int{5, 1}},
			{StudentID: "new", PublicName: "Новичок", SolvedCountBySite: []int{2, 0}},
		},
		Contests: []domain.GeneratedContestStandings{
			{ID: "built", Rows: []domain.GeneratedRow{
				{StudentID: "old", PublicName: "Новое Имя", Statuses: []string{"solved"}},
				{StudentID: "new", PublicName: "Новичок", Statuses: []string{"none"}},
			}},
		},
	}

	merged, ok := p.mergeWithNonUpdatedContests(group, freshBuild, students)
	if !ok {
		t.Fatal("merge failed")
	}

	// Доска почёта — свежая, с новым учеником.
	if len(merged.SolvedSummary) != 2 || len(merged.SolvedSummarySites) != 2 {
		t.Fatalf("summary must be fresh: %+v", merged.SolvedSummary)
	}

	if len(merged.Contests) != 2 {
		t.Fatalf("expected 2 contests, got %d", len(merged.Contests))
	}
	frozen := merged.Contests[1]
	if frozen.ID != "frozen" {
		t.Fatalf("contest order broken: %+v", merged.Contests)
	}
	if len(frozen.Rows) != 2 {
		t.Fatalf("frozen rows must match roster (old+new): %+v", frozen.Rows)
	}
	// Результаты старого ученика не тронуты, имя обновлено.
	if frozen.Rows[0].StudentID != "old" || frozen.Rows[0].SolvedCount != 2 || frozen.Rows[0].PublicName != "Новое Имя" {
		t.Fatalf("existing row damaged: %+v", frozen.Rows[0])
	}
	// Новичок — пустая строка нужной длины.
	if frozen.Rows[1].StudentID != "new" || frozen.Rows[1].SolvedCount != 0 || len(frozen.Rows[1].Statuses) != 2 {
		t.Fatalf("new student row wrong: %+v", frozen.Rows[1])
	}
	// Выбывший удалён.
	for _, row := range frozen.Rows {
		if row.StudentID == "gone" {
			t.Fatalf("removed student still present: %+v", frozen.Rows)
		}
	}
}

// Легаси-таблица без student_id в строках: состав не трогаем, чтобы ничего не потерять.
func TestReconcileSkipsLegacyRowsWithoutStudentID(t *testing.T) {
	contest := domain.GeneratedContestStandings{
		ID:    "legacy",
		Tasks: []domain.GeneratedTask{{Label: "A"}},
		Rows: []domain.GeneratedRow{
			{PublicName: "Без Айди", SolvedCount: 1, Statuses: []string{"solved"}},
		},
	}
	group := domain.GroupDefinition{StudentIDs: []string{"s1"}}
	reconcileContestRoster(&contest, group, map[string]domain.Student{"s1": {ID: "s1", PublicName: "Новый"}})
	if len(contest.Rows) != 1 || contest.Rows[0].PublicName != "Без Айди" {
		t.Fatalf("legacy rows must stay untouched: %+v", contest.Rows)
	}
}

// Группа целиком из update=false контестов больше не выпадает из генерации:
// selectGroupsWithUpdatableContests оставляет её (пересборки контестов не будет,
// но состав, доска почёта и оценки обновятся).
func TestSelectGroupsKeepsFrozenGroups(t *testing.T) {
	groups := []domain.GroupDefinition{
		{Slug: "frozen", Contests: []domain.GroupContestRef{upd(false)}},
		{Slug: "empty"},
		{Slug: "normal", Contests: []domain.GroupContestRef{upd(true), upd(false)}},
	}
	out := selectGroupsWithUpdatableContests(groups)
	if len(out) != 2 || out[0].Slug != "frozen" || out[1].Slug != "normal" {
		t.Fatalf("unexpected selection: %+v", out)
	}
	// Контесты не фильтруются — builder сам решает, что пересобирать.
	if len(out[1].Contests) != 2 {
		t.Fatalf("contests must not be filtered: %+v", out[1].Contests)
	}
}
