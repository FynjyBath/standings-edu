package standings

import (
	"context"
	"io"
	"log"
	"testing"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// Фейковый codeforces-contest-провайдер: возвращает контест с именованными
// задачами (как реальный из contest.status/standings).
type fakeCFContestProvider struct{}

func (fakeCFContestProvider) ProviderID() string { return source.CodeforcesContestProviderID }

func (fakeCFContestProvider) BuildStandings(_ context.Context, in source.ContestProviderInput) (domain.GeneratedContestStandings, error) {
	tasks := []domain.GeneratedTask{
		{Label: "A", URL: "https://codeforces.com/contest/1998/problem/A", NormalizedURL: "cf/1998/a", Name: "Задача Альфа"},
		{Label: "B", URL: "https://codeforces.com/contest/1998/problem/B", NormalizedURL: "cf/1998/b", Name: "Задача Бета"},
	}
	return domain.GeneratedContestStandings{
		ScoreSystem: in.Contest.ScoreSystem.Normalized(),
		Subcontests: []domain.GeneratedSubcontest{{Title: "Результаты", TaskCount: len(tasks), Tasks: tasks}},
		Tasks:       tasks,
	}, nil
}

// Контест, добавленный ссылкой на codeforces-контест, при разворачивании должен
// сохранять названия задач (для подсказки в заголовке колонки).
func TestBuildTaskContestExpandsCodeforcesTaskNames(t *testing.T) {
	reg := source.NewRegistry()
	reg.RegisterProvider(fakeCFContestProvider{})
	b := NewBuilder(reg, log.New(io.Discard, "", 0), 2)

	data := &domain.SourceData{
		Students: map[string]domain.Student{"s1": {ID: "s1", PublicName: "Иван"}},
		Contests: map[string]domain.Contest{
			"c1": {
				ID: "c1", Title: "Тренировка", ScoreSystem: domain.ScoreSystemEdu, ContestType: domain.ContestTypeTasks,
				Subcontests: []domain.Subcontest{{Title: "Задачи", Tasks: []string{"https://codeforces.com/contest/1998"}}},
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
	if c.Tasks[0].Name != "Задача Альфа" || c.Tasks[1].Name != "Задача Бета" {
		t.Fatalf("названия задач не пробросились при разворачивании CF-контеста: %q, %q", c.Tasks[0].Name, c.Tasks[1].Name)
	}
}

// Отдельная ссылка на задачу CF (…/problem/<idx>) получает название задачи по
// индексу из развёрнутого контеста.
func TestBuildTaskContestNamesIndividualCodeforcesProblem(t *testing.T) {
	reg := source.NewRegistry()
	reg.RegisterProvider(fakeCFContestProvider{})
	b := NewBuilder(reg, log.New(io.Discard, "", 0), 2)

	data := &domain.SourceData{
		Students: map[string]domain.Student{"s1": {ID: "s1", PublicName: "Иван"}},
		Contests: map[string]domain.Contest{
			"c1": {
				ID: "c1", Title: "Отдельные задачи", ScoreSystem: domain.ScoreSystemEdu, ContestType: domain.ContestTypeTasks,
				Subcontests: []domain.Subcontest{{Title: "Задачи", Tasks: []string{
					"https://codeforces.com/contest/1998/problem/B",
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
	if len(c.Tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(c.Tasks))
	}
	if c.Tasks[0].Name != "Задача Бета" {
		t.Fatalf("отдельная CF-задача не получила имя по индексу: %q", c.Tasks[0].Name)
	}
}
