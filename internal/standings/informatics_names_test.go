package standings

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// Фейковый informatics-клиент: разворачивает сборник в задачи с заголовками
// (как реальный из оглавления statements_toc).
type fakeInformaticsExpander struct{}

func (fakeInformaticsExpander) FetchUserResults(context.Context, string) ([]source.TaskResult, error) {
	return nil, nil
}
func (fakeInformaticsExpander) SupportsTaskScores() bool { return false }
func (fakeInformaticsExpander) MatchTaskURL(u string) bool {
	return strings.Contains(strings.ToLower(u), "informatics.msk.ru")
}
func (fakeInformaticsExpander) FetchStatementProblems(_ context.Context, _ int) ([]source.InformaticsStatementProblem, error) {
	return []source.InformaticsStatementProblem{
		{ChapterID: 111, Title: "A. Гипотенуза"},
		{ChapterID: 222, Title: "B. Следующее"},
	}, nil
}

// Контест, добавленный ссылкой на сборник informatics, при разворачивании должен
// сохранять названия задач (заголовки из оглавления) для подсказки в колонке.
func TestBuildTaskContestExpandsInformaticsStatementNames(t *testing.T) {
	reg := source.NewRegistry()
	reg.RegisterSite("informatics", fakeInformaticsExpander{})
	b := NewBuilder(reg, log.New(io.Discard, "", 0), 2)

	data := &domain.SourceData{
		Students: map[string]domain.Student{"s1": {ID: "s1", PublicName: "Иван"}},
		Contests: map[string]domain.Contest{
			"c1": {
				ID: "c1", Title: "Сборник", ScoreSystem: domain.ScoreSystemEdu, ContestType: domain.ContestTypeTasks,
				Subcontests: []domain.Subcontest{{Title: "Задачи", Tasks: []string{
					"https://informatics.msk.ru/mod/statements/view.php?id=928",
				}}},
			},
		},
	}
	groups := []domain.GroupDefinition{{
		Slug: "g1", Title: "G1", StudentIDs: []string{"s1"},
		Contests: []domain.GroupContestRef{{ID: "c1", Update: true}},
	}}

	res, _, err := b.BuildGroupsStandings(context.Background(), data, groups)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	c := res["g1"].Contests[0]
	if len(c.Tasks) != 2 {
		t.Fatalf("want 2 expanded tasks, got %d", len(c.Tasks))
	}
	if c.Tasks[0].Name != "A. Гипотенуза" || c.Tasks[1].Name != "B. Следующее" {
		t.Fatalf("названия задач не пробросились при разворачивании сборника: %q, %q", c.Tasks[0].Name, c.Tasks[1].Name)
	}
}
