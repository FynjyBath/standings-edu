package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Мок informatics: отдаёт заданный набор прогонов (страницами по 100).
func newRunsServer(t *testing.T, runsRef *[]map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/index.php", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`name="logintoken" value="x"`))
	})
	mux.HandleFunc("/py/problem/0/filter-runs", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		runs := *runsRef
		from := (page - 1) * informaticsRunsPageSize
		to := from + informaticsRunsPageSize
		if from > len(runs) {
			from = len(runs)
		}
		if to > len(runs) {
			to = len(runs)
		}
		pageCount := (len(runs) + informaticsRunsPageSize - 1) / informaticsRunsPageSize
		if pageCount == 0 {
			pageCount = 1
		}
		json.NewEncoder(w).Encode(map[string]any{
			"result": "success",
			"data":   runs[from:to],
			"metadata": map[string]any{
				"page_count": pageCount,
			},
		})
	})
	return httptest.NewServer(mux)
}

func mkRun(id, problemID, status int, at string) map[string]any {
	return map[string]any{
		"id":            id,
		"ejudge_status": status,
		"ejudge_score":  nil,
		"score":         nil,
		"create_time":   at,
		"problem":       map[string]any{"id": problemID},
	}
}

// Сценарий «последняя информация — самая актуальная»: посылка сначала пришла
// незавершённой (в очереди), затем в следующем заборе — с финальным OK. Свежие
// данные перезаписывают сохранённые, watermark двигается всегда.
func TestInformaticsVerdictOverwrite(t *testing.T) {
	// Забор 1: run 200 в очереди (11), run 100 — WA.
	runs := []map[string]any{
		mkRun(200, 7, 11, "2026-07-01T10:05:00+00:00"),
		mkRun(100, 7, 5, "2026-07-01T10:00:00+00:00"),
	}
	srv := newRunsServer(t, &runs)
	defer srv.Close()

	c, err := NewInformaticsAPIClientWithState(InformaticsCredentials{Username: "u", Password: "p"}, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL
	c.loggedIn = true
	c.lastLogin = time.Now()

	res, err := c.FetchUserResults(context.Background(), "77")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Solved {
		t.Fatalf("после забора 1: задача с попыткой (WA), очередь не считается: %+v", res)
	}

	// Watermark должен продвинуться до 200 несмотря на незавершённый статус.
	st, ok, _ := c.getAccountState("77")
	if !ok || st.MaxRunID != 200 {
		t.Fatalf("watermark должен дойти до 200: %+v", st)
	}
	if len(st.Runs) != 2 {
		t.Fatalf("обе посылки должны сохраниться: %+v", st.Runs)
	}

	// Забор 2: та же посылка 200 теперь OK (страница снова её содержит) +
	// новая 300 (WA по другой задаче).
	runs = []map[string]any{
		mkRun(300, 9, 5, "2026-07-01T11:00:00+00:00"),
		mkRun(200, 7, 0, "2026-07-01T10:05:00+00:00"), // перетестирована: OK
		mkRun(100, 7, 5, "2026-07-01T10:00:00+00:00"),
	}
	res, err = c.FetchUserResults(context.Background(), "77")
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]TaskResult{}
	for _, r := range res {
		byURL[r.TaskURL] = r
	}
	task7 := byURL[c.buildTaskURL(7)]
	if !task7.Solved {
		t.Fatalf("вердикт посылки 200 должен быть ПЕРЕЗАПИСАН на OK: %+v", task7)
	}
	if byURL[c.buildTaskURL(9)].Solved {
		t.Fatalf("задача 9 — только попытка: %+v", byURL[c.buildTaskURL(9)])
	}
	st, _, _ = c.getAccountState("77")
	if st.MaxRunID != 300 || len(st.Runs) != 3 {
		t.Fatalf("state после забора 2: %+v", st)
	}
	// Запись run 200 обновлена (перезаписана статусом OK).
	for _, r := range st.Runs {
		if r.ID == 200 && r.Status != 0 {
			t.Fatalf("stored run 200 должен иметь статус OK: %+v", r)
		}
	}
}

// Понижение вердикта тоже перезаписывается (OK → снят при перетестировании).
func TestInformaticsVerdictDowngrade(t *testing.T) {
	runs := []map[string]any{
		mkRun(100, 7, 0, "2026-07-01T10:00:00+00:00"), // OK
	}
	srv := newRunsServer(t, &runs)
	defer srv.Close()

	c, err := NewInformaticsAPIClientWithState(InformaticsCredentials{Username: "u", Password: "p"}, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL
	c.loggedIn = true
	c.lastLogin = time.Now()

	if res, _ := c.FetchUserResults(context.Background(), "77"); len(res) != 1 || !res[0].Solved {
		t.Fatalf("до перетеста задача решена: %+v", res)
	}

	// Перетестирование: тот же run теперь WA; страница содержит его же + новую посылку.
	runs = []map[string]any{
		mkRun(101, 8, 0, "2026-07-01T11:00:00+00:00"), // новая, другая задача
		mkRun(100, 7, 5, "2026-07-01T10:00:00+00:00"), // снята: WA
	}
	res, err := c.FetchUserResults(context.Background(), "77")
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]TaskResult{}
	for _, r := range res {
		byURL[r.TaskURL] = r
	}
	if byURL[c.buildTaskURL(7)].Solved {
		t.Fatalf("вердикт должен быть понижен до попытки: %+v", byURL[c.buildTaskURL(7)])
	}
}
