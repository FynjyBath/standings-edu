package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// runsPageServer отдаёт informatics filter-runs: page 1..(pageCount) с данными,
// а страницы из failPages всегда 500-ят (эмуляция стабильно битой глубокой
// страницы у informatics).
func runsPageServer(t *testing.T, pageCount int, failPages map[int]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		var p int
		fmt.Sscanf(page, "%d", &p)
		if failPages[p] {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("<html>500</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// У каждой страницы свой убывающий run id, чтобы забор шёл от свежих к старым.
		id := (pageCount-p+1)*100 + 1
		fmt.Fprintf(w, `{"result":"success","data":[{"id":%d,"ejudge_status":0,"create_time":"2026-07-01T09:00:00+00:00","problem":{"id":5}}],"metadata":{"page_count":%d,"count":1}}`, id, pageCount)
	}))
}

// Полный первичный забор, где последняя (старая) страница стабильно 500-ит:
// возвращаются свежие страницы, complete=false, ошибки нет.
func TestCollectRunsIncompleteOnDeepPage500(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	srv := runsPageServer(t, 6, map[int]bool{6: true})
	defer srv.Close()
	c := &InformaticsAPIClient{baseURL: srv.URL, httpClient: srv.Client(), loggedIn: true}

	runs, complete, err := c.collectNewInformaticsRuns(context.Background(), "207033", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if complete {
		t.Fatal("scan must be marked incomplete when a page 500s")
	}
	if len(runs) == 0 {
		t.Fatal("must still return runs from the pages that succeeded")
	}
	// Страницы 1..5 отдали по одной посылке.
	if len(runs) != 5 {
		t.Fatalf("expected 5 runs from pages 1-5, got %d", len(runs))
	}
}

// Полный забор без сбоев — complete=true, все страницы собраны.
func TestCollectRunsCompleteWhenAllPagesOK(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	srv := runsPageServer(t, 6, nil)
	defer srv.Close()
	c := &InformaticsAPIClient{baseURL: srv.URL, httpClient: srv.Client(), loggedIn: true}

	runs, complete, err := c.collectNewInformaticsRuns(context.Background(), "207033", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !complete {
		t.Fatal("scan must be complete when all pages succeed")
	}
	if len(runs) != 6 {
		t.Fatalf("expected 6 runs, got %d", len(runs))
	}
}
