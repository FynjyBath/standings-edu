package standings

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// Мок ejudge: список задач контеста + прогоны (master list-runs).
func mockEjudgeServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/ej/api/v1/client/contest-status-json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
			"problems": []map[string]any{
				{"id": 1, "short_name": "A", "long_name": "Alpha"},
				{"id": 2, "short_name": "B", "long_name": "Beta"},
			},
		}})
	})
	mux.HandleFunc("/ej/api/v1/master/list-runs-json", func(w http.ResponseWriter, r *http.Request) {
		cid, _ := strconv.Atoi(r.URL.Query().Get("contest_id"))
		_ = cid
		at := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).Unix()
		runs := []map[string]any{
			{"run_id": 0, "run_time": at, "prob_id": 1, "status": 0, "score": 100, "user_login": "ivan"},
			{"run_id": 1, "run_time": at, "prob_id": 2, "status": 5, "score": 0, "user_login": "ivan"},
			{"run_id": 2, "run_time": at, "prob_id": 1, "status": 0, "score": 100, "user_login": "petr"},
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"runs": runs, "total_runs": len(runs)}})
	})
	return httptest.NewServer(mux)
}

func TestBuildGroupsWithEjudgeContest(t *testing.T) {
	srv := mockEjudgeServer()
	defer srv.Close()

	client, err := source.NewEjudgeClient(source.EjudgeInstanceConfig{EjudgeID: "kodu", BaseURL: srv.URL, APIKey: "K"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	reg := source.NewRegistry()
	reg.RegisterSite("kodu", client)
	b := NewBuilder(reg, log.New(io.Discard, "", 0), 2)

	data := &domain.SourceData{
		Students: map[string]domain.Student{
			"s1": {ID: "s1", PublicName: "Иван", Accounts: []domain.Account{{Site: "kodu", AccountID: "ivan"}}},
			"s2": {ID: "s2", PublicName: "Пётр", Accounts: []domain.Account{{Site: "kodu", AccountID: "petr"}}},
		},
		Contests: map[string]domain.Contest{
			"c1": {ID: "c1", Title: "Ejudge", ScoreSystem: domain.ScoreSystemIOI, ContestType: domain.ContestTypeTasks,
				Subcontests: []domain.Subcontest{{Title: "Задачи", Tasks: []string{
					srv.URL + "/new-client?contest_id=25408",
				}}}},
		},
	}
	groups := []domain.GroupDefinition{{Slug: "g1", Title: "G1", StudentIDs: []string{"s1", "s2"},
		Contests: []domain.GroupContestRef{{ID: "c1", Update: true}}}}

	res, _, err := b.BuildGroupsStandings(context.Background(), data, groups)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	c := res["g1"].Contests[0]
	if len(c.Tasks) != 2 {
		t.Fatalf("contest must expand into 2 problems, got %d", len(c.Tasks))
	}
	// Названия задач (long_name) прокидываются в подсказку заголовка колонки.
	if c.Tasks[0].Name != "Alpha" || c.Tasks[1].Name != "Beta" {
		t.Fatalf("task names must carry ejudge long_name: %q, %q", c.Tasks[0].Name, c.Tasks[1].Name)
	}
	byStudent := map[string]domain.GeneratedRow{}
	for _, row := range c.Rows {
		byStudent[row.StudentID] = row
	}
	// Иван: A решено (100), B попытка -> solved=1, score=100.
	if r := byStudent["s1"]; r.SolvedCount != 1 || r.TotalScore != 100 {
		t.Fatalf("ivan wrong: %+v", r)
	}
	// Пётр: A решено (100), B не сдавал -> solved=1, score=100.
	if r := byStudent["s2"]; r.SolvedCount != 1 || r.TotalScore != 100 {
		t.Fatalf("petr wrong: %+v", r)
	}
	// Первая колонка (A) у Ивана — solved, вторая (B) — attempted.
	if byStudent["s1"].Statuses[0] != domain.TaskStatusSolved || byStudent["s1"].Statuses[1] != domain.TaskStatusAttempted {
		t.Fatalf("ivan statuses wrong: %v", byStudent["s1"].Statuses)
	}
}
