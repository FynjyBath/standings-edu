package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

// mockEjudge поднимает httptest-сервер, отвечающий как API ejudge: контест-статус
// (список задач) и master list-runs (прогоны, с пагинацией по run_id и проверкой
// Bearer-ключа).
type mockEjudge struct {
	problems map[int][]EjudgeProblem
	runs     map[int][]ejudgeRun
	wantKey  string
	authSeen []string
}

func (m *mockEjudge) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ej/api/v1/client/contest-status-json", func(w http.ResponseWriter, r *http.Request) {
		m.authSeen = append(m.authSeen, r.Header.Get("Authorization"))
		cid, _ := strconv.Atoi(r.URL.Query().Get("contest_id"))
		type prob struct {
			ID        int    `json:"id"`
			ShortName string `json:"short_name"`
			LongName  string `json:"long_name"`
		}
		probs := make([]prob, 0)
		for _, p := range m.problems[cid] {
			probs = append(probs, prob{ID: p.ProbID, ShortName: p.ShortName, LongName: p.LongName})
		}
		writeEjudgeReply(w, map[string]any{"problems": probs})
	})
	mux.HandleFunc("/ej/api/v1/master/list-runs-json", func(w http.ResponseWriter, r *http.Request) {
		m.authSeen = append(m.authSeen, r.Header.Get("Authorization"))
		if m.wantKey != "" && r.Header.Get("Authorization") != "Bearer "+m.wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			writeEjudgeError(w, "bad key")
			return
		}
		cid, _ := strconv.Atoi(r.URL.Query().Get("contest_id"))
		first, _ := strconv.Atoi(r.URL.Query().Get("first_run"))
		last, _ := strconv.Atoi(r.URL.Query().Get("last_run"))
		all := m.runs[cid]
		page := make([]ejudgeRun, 0)
		for _, run := range all {
			if run.RunID >= first && run.RunID <= last {
				page = append(page, run)
			}
		}
		writeEjudgeReply(w, map[string]any{"runs": page, "total_runs": len(all)})
	})
	return mux
}

func writeEjudgeReply(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result, "server_time": 1})
}

func writeEjudgeError(w http.ResponseWriter, msg string) {
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"message": msg}})
}

func newTestEjudgeClient(t *testing.T, srv *httptest.Server, key string) *EjudgeClient {
	t.Helper()
	c, err := NewEjudgeClient(EjudgeInstanceConfig{EjudgeID: "kodu", BaseURL: srv.URL, APIKey: key})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func TestEjudgeFetchContestProblems(t *testing.T) {
	mock := &mockEjudge{problems: map[int][]EjudgeProblem{
		25408: {{ProbID: 1, ShortName: "A", LongName: "Alpha"}, {ProbID: 2, ShortName: "B", LongName: "Beta"}},
	}}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestEjudgeClient(t, srv, "K1")

	probs, err := c.FetchContestProblems(context.Background(), 25408)
	if err != nil {
		t.Fatalf("fetch problems: %v", err)
	}
	if len(probs) != 2 || probs[0].ProbID != 1 || probs[1].ShortName != "B" {
		t.Fatalf("problems wrong: %+v", probs)
	}
}

func TestEjudgeFetchUserResults(t *testing.T) {
	solvedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	mock := &mockEjudge{
		wantKey: "K1",
		runs: map[int][]ejudgeRun{
			25408: {
				{RunID: 0, RunTime: solvedAt.Unix(), ProbID: 1, Status: 0, Score: 100, UserLogin: "ivan"}, // OK
				{RunID: 1, RunTime: solvedAt.Unix(), ProbID: 2, Status: 5, Score: 0, UserLogin: "ivan"},   // WA
				{RunID: 2, RunTime: solvedAt.Unix(), ProbID: 1, Status: 96, Score: 0, UserLogin: "petr"},  // running -> skip
				{RunID: 3, RunTime: solvedAt.Unix(), ProbID: 2, Status: 0, Score: 80, UserLogin: "petr"},  // OK 80
			},
		},
	}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestEjudgeClient(t, srv, "K1")
	c.ObserveTaskURL(srv.URL + "/new-client?contest_id=25408")

	ivan, err := c.FetchUserResults(context.Background(), "ivan")
	if err != nil {
		t.Fatalf("fetch ivan: %v", err)
	}
	byURL := map[string]TaskResult{}
	for _, r := range ivan {
		byURL[r.TaskURL] = r
	}
	p1 := c.problemURL(25408, 1)
	p2 := c.problemURL(25408, 2)
	if r := byURL[p1]; !r.Solved || r.Score == nil || *r.Score != 100 || len(r.Timed) != 1 {
		t.Fatalf("ivan p1 wrong: %+v", r)
	}
	if r := byURL[p2]; r.Solved || !r.Attempted {
		t.Fatalf("ivan p2 must be attempted-not-solved: %+v", r)
	}

	// Регистр логина не важен: "IVAN" находит те же результаты.
	if upper, _ := c.FetchUserResults(context.Background(), "IVAN"); len(upper) != len(ivan) {
		t.Fatalf("login match must be case-insensitive: %d vs %d", len(upper), len(ivan))
	}

	petr, _ := c.FetchUserResults(context.Background(), "petr")
	byURL = map[string]TaskResult{}
	for _, r := range petr {
		byURL[r.TaskURL] = r
	}
	// p1 у petr — только pending прогон, поэтому его нет вовсе.
	if _, ok := byURL[p1]; ok {
		t.Fatalf("petr p1 (only pending) must be absent: %+v", byURL[p1])
	}
	if r := byURL[p2]; !r.Solved || *r.Score != 80 {
		t.Fatalf("petr p2 wrong: %+v", r)
	}
}

func TestEjudgePagination(t *testing.T) {
	runs := make([]ejudgeRun, 0, ejudgeListRunsPageSize+50)
	for i := 0; i < ejudgeListRunsPageSize+50; i++ {
		runs = append(runs, ejudgeRun{RunID: i, RunTime: 1, ProbID: 1, Status: 5, Score: 0, UserLogin: "ivan"})
	}
	// последний — OK, чтобы проверить, что вторая страница дошла.
	runs[len(runs)-1] = ejudgeRun{RunID: len(runs) - 1, RunTime: 1, ProbID: 1, Status: 0, Score: 100, UserLogin: "ivan"}
	mock := &mockEjudge{runs: map[int][]ejudgeRun{1: runs}}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestEjudgeClient(t, srv, "")
	c.ObserveTaskURL(srv.URL + "/new-client?contest_id=1")

	res, err := c.FetchUserResults(context.Background(), "ivan")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(res) != 1 || !res[0].Solved {
		t.Fatalf("pagination must fetch the last-page OK run: %+v", res)
	}
	if len(res[0].Timed) != ejudgeListRunsPageSize+50 {
		t.Fatalf("all %d runs must be aggregated, got %d timed", ejudgeListRunsPageSize+50, len(res[0].Timed))
	}
}

func TestEjudgeMatchTaskURL(t *testing.T) {
	c, _ := NewEjudgeClient(EjudgeInstanceConfig{EjudgeID: "kodu", BaseURL: "https://ej.kod-u.ru", APIKey: "K"})
	yes := []string{
		"https://ej.kod-u.ru/new-client?contest_id=25408",
		"https://ej.kod-u.ru/new-client?contest_id=25408&prob_id=3",
		"http://ej.kod-u.ru/new-client?SID=abc&contest_id=25408&action=1",
	}
	for _, u := range yes {
		if !c.MatchTaskURL(u) {
			t.Errorf("must match: %s", u)
		}
	}
	no := []string{
		"https://other.host/new-client?contest_id=1", // другой хост
		"https://ej.kod-u.ru/new-client",             // нет contest_id
		"https://codeforces.com/contest/1711",        // не ejudge
	}
	for _, u := range no {
		if c.MatchTaskURL(u) {
			t.Errorf("must NOT match: %s", u)
		}
	}
}

func TestLoadEjudgeInstances(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/ej.json"
	writeFile(t, path, `[{"ejudge_id":"kodu","base_url":"https://ej.kod-u.ru","api_key":"K1"},
	                     {"ejudge_id":"other","base_url":"https://ej.example.org","api_key":"K2"}]`)
	got, err := LoadEjudgeInstances(path)
	if err != nil || len(got) != 2 || got[0].EjudgeID != "kodu" {
		t.Fatalf("load: %+v err=%v", got, err)
	}

	// нет файла — не ошибка
	if got, err := LoadEjudgeInstances(dir + "/nope.json"); err != nil || got != nil {
		t.Fatalf("missing file must be (nil,nil): %+v %v", got, err)
	}
	// дубликат ejudge_id — ошибка
	writeFile(t, path, `[{"ejudge_id":"a","base_url":"https://x"},{"ejudge_id":"a","base_url":"https://y"}]`)
	if _, err := LoadEjudgeInstances(path); err == nil {
		t.Fatal("duplicate ejudge_id must fail")
	}
	// пустой ejudge_id — ошибка
	writeFile(t, path, `[{"ejudge_id":"","base_url":"https://x"}]`)
	if _, err := LoadEjudgeInstances(path); err == nil {
		t.Fatal("empty ejudge_id must fail")
	}
	// регистр не спасает от коллизии: "Kodu" и "kodu" — один сайт.
	writeFile(t, path, `[{"ejudge_id":"Kodu","base_url":"https://x"},{"ejudge_id":"kodu","base_url":"https://y"}]`)
	if _, err := LoadEjudgeInstances(path); err == nil {
		t.Fatal("case-insensitive duplicate ejudge_id must fail")
	}
	// ejudge_id приводится к нижнему регистру.
	writeFile(t, path, `[{"ejudge_id":"KodU","base_url":"https://x"}]`)
	got, err = LoadEjudgeInstances(path)
	if err != nil || len(got) != 1 || got[0].EjudgeID != "kodu" {
		t.Fatalf("ejudge_id must be lowercased: %+v %v", got, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
