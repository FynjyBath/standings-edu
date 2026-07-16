package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/web"
)

// Временный перф-замер: страница группы на масштабе 800 учеников × 41 контест.
func makeBigStandings(nStudents, nContests, nTasks int) domain.GeneratedGroupStandings {
	std := domain.GeneratedGroupStandings{GroupSlug: "big", GroupTitle: "Большая"}
	now := time.Now()
	for c := 0; c < nContests; c++ {
		contest := domain.GeneratedContestStandings{
			ID: fmt.Sprintf("c%d", c), Title: fmt.Sprintf("Контест %d", c),
			ScoreSystem: domain.ScoreSystemEdu, GeneratedAt: &now,
		}
		tasks := make([]domain.GeneratedTask, nTasks)
		for t := 0; t < nTasks; t++ {
			u := fmt.Sprintf("https://informatics.msk.ru/mod/statements/view.php?chapterid=%d#1", c*100+t)
			tasks[t] = domain.GeneratedTask{Label: domain.AlphabetLabel(t), URL: u, NormalizedURL: u, Name: "Задача"}
		}
		contest.Tasks = tasks
		contest.Subcontests = []domain.GeneratedSubcontest{{Title: "Задачи", TaskCount: nTasks, Tasks: tasks}}
		rows := make([]domain.GeneratedRow, nStudents)
		for s := 0; s < nStudents; s++ {
			st := make([]string, nTasks)
			ups := make([]bool, nTasks)
			for t := 0; t < nTasks; t++ {
				switch (s + t + c) % 3 {
				case 0:
					st[t] = "solved"
				case 1:
					st[t] = "attempted"
				default:
					st[t] = "none"
				}
				ups[t] = (s+t)%5 == 0
			}
			rows[s] = domain.GeneratedRow{
				StudentID: fmt.Sprintf("s%d", s), PublicName: fmt.Sprintf("Ученик %04d", s),
				Place: fmt.Sprintf("%d", s+1), SolvedCount: nTasks / 3, Statuses: st, Upsolved: ups,
				Accounts: map[string]string{"informatics": fmt.Sprintf("%d", 100000+s)},
			}
		}
		contest.Rows = rows
		std.Contests = append(std.Contests, contest)
	}
	// Доска почёта.
	std.SolvedSummarySites = []string{"informatics"}
	for s := 0; s < nStudents; s++ {
		std.SolvedSummary = append(std.SolvedSummary, domain.GeneratedGroupSolvedSummaryRow{
			StudentID: fmt.Sprintf("s%d", s), PublicName: fmt.Sprintf("Ученик %04d", s),
			SolvedCountOnPageSites: s % 100, TotalSolvedCount: s % 100, SolvedCountBySite: []int{s % 100},
		})
	}
	return std
}

func TestPerfProbeBigGroupPage(t *testing.T) {
	if os.Getenv("PERF_PROBE") == "" {
		t.Skip("перф-замер: запуск PERF_PROBE=1 go test -run TestPerfProbe")
	}
	std := makeBigStandings(800, 41, 8)

	// 1) CloneForServe — выполняется на КАЖДЫЙ запрос страницы/API.
	t0 := time.Now()
	clone := std.CloneForServe()
	dClone := time.Since(t0)

	// 2) Рендер страницы как в проде: ParseFiles на каждый запрос.
	renderer := web.NewTemplateRenderer("../../web/templates")
	page := GroupPageData{PageTitle: "t", Standings: clone, Footer: FooterInfo{}}
	rec := httptest.NewRecorder()
	t1 := time.Now()
	if err := renderer.Render(rec, 200, "group_standings.html", page); err != nil {
		t.Fatalf("render: %v", err)
	}
	dRender := time.Since(t1)
	htmlSize := rec.Body.Len()

	// 3) Повторный рендер (тёплый ФС-кэш) — отделить парсинг шаблонов.
	rec2 := httptest.NewRecorder()
	t2 := time.Now()
	_ = renderer.Render(rec2, 200, "group_standings.html", page)
	dRender2 := time.Since(t2)

	fmt.Printf("PERF clone_for_serve: %v\n", dClone)
	fmt.Printf("PERF render_full(parse+exec): %v; repeat: %v\n", dRender, dRender2)
	fmt.Printf("PERF html_size: %.1f MB\n", float64(htmlSize)/1024/1024)
}

func TestPerfProbeMediumGroupPage(t *testing.T) {
	if os.Getenv("PERF_PROBE") == "" {
		t.Skip("перф-замер: запуск PERF_PROBE=1 go test -run TestPerfProbe")
	}
	std := makeBigStandings(80, 41, 8)
	renderer := web.NewTemplateRenderer("../../web/templates")
	page := GroupPageData{PageTitle: "t", Standings: std.CloneForServe(), Footer: FooterInfo{}}
	rec := httptest.NewRecorder()
	t1 := time.Now()
	if err := renderer.Render(rec, 200, "group_standings.html", page); err != nil {
		t.Fatalf("render: %v", err)
	}
	fmt.Printf("PERF medium(80x41) render: %v; html: %.1f MB\n", time.Since(t1), float64(rec.Body.Len())/1024/1024)
}

func TestPerfProbeJSONSize(t *testing.T) {
	if os.Getenv("PERF_PROBE") == "" {
		t.Skip("перф-замер: запуск PERF_PROBE=1 go test -run TestPerfProbe")
	}
	std := makeBigStandings(800, 41, 8)
	t0 := time.Now()
	b, err := jsonMarshalProbe(std)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PERF standings JSON (800x41): %.1f MB, marshal %v\n", float64(len(b))/1024/1024, time.Since(t0))
	std2 := makeBigStandings(80, 41, 8)
	b2, _ := jsonMarshalProbe(std2)
	fmt.Printf("PERF standings JSON (80x41): %.1f MB (встраивается в /summary страницу)\n", float64(len(b2))/1024/1024)
}

func jsonMarshalProbe(v any) ([]byte, error) { return json.Marshal(v) }
