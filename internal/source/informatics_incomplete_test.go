package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mockInformaticsRuns эмулирует /py/problem/0/filter-runs: всего total прогонов
// (новые — с меньшим офсетом), сервер режет count по 100 и поддерживает офсеты;
// запрос, диапазон которого [offset, offset+count) задевает «битый» офсет, отдаёт
// 500 (как у реального informatics на отдельных неоткрывающихся записях).
func mockInformaticsRuns(t *testing.T, total int, broken map[int]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if count > 100 {
			count = 100 // серверный потолок размера страницы
		}
		if count < 1 || page < 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		offset := (page - 1) * count
		end := offset + count
		if end > total {
			end = total
		}
		for o := offset; o < end; o++ {
			if broken[o] {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("<html>500</html>"))
				return
			}
		}
		pageCount := (total + count - 1) / count
		var b strings.Builder
		fmt.Fprintf(&b, `{"result":"success","metadata":{"page_count":%d,"count":%d},"data":[`, pageCount, count)
		for o := offset; o < end; o++ {
			if o > offset {
				b.WriteByte(',')
			}
			id := total - o // офсет 0 — самый свежий (наибольший id)
			fmt.Fprintf(&b, `{"id":%d,"ejudge_status":0,"create_time":"2026-07-01T09:00:00+00:00","problem":{"id":5}}`, id)
		}
		b.WriteString(`]}`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(b.String()))
	}))
}

// Одна битая запись глубоко в истории: страница count=100 падает, но дроблением
// 100→10→1 спасаются все прогоны, кроме той одной. lostRuns=1.
func TestSalvageSingleBrokenRun(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	const total = 583
	srv := mockInformaticsRuns(t, total, map[int]bool{571: true})
	defer srv.Close()
	c := &InformaticsAPIClient{baseURL: srv.URL, httpClient: srv.Client(), loggedIn: true}

	runs, lost, err := c.collectNewInformaticsRuns(context.Background(), "207033", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lost != 1 {
		t.Fatalf("expected exactly 1 unrecoverable run, got %d", lost)
	}
	if len(runs) != total-1 {
		t.Fatalf("expected %d salvaged runs, got %d", total-1, len(runs))
	}
	// Битую запись (id = total-571) забирать не должны.
	brokenID := total - 571
	for _, r := range runs {
		if r.ID == brokenID {
			t.Fatalf("broken run id=%d must not be present", brokenID)
		}
	}
}

// Несколько битых записей в одном 10-блоке: спасаем всё, кроме них.
func TestSalvageSeveralBrokenRuns(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	const total = 583
	broken := map[int]bool{571: true, 573: true, 579: true}
	srv := mockInformaticsRuns(t, total, broken)
	defer srv.Close()
	c := &InformaticsAPIClient{baseURL: srv.URL, httpClient: srv.Client(), loggedIn: true}

	runs, lost, err := c.collectNewInformaticsRuns(context.Background(), "207033", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lost != len(broken) {
		t.Fatalf("expected %d unrecoverable runs, got %d", len(broken), lost)
	}
	if len(runs) != total-len(broken) {
		t.Fatalf("expected %d salvaged runs, got %d", total-len(broken), len(runs))
	}
}

// Всё цело — забор полный, ничего не теряем.
func TestSalvageNothingBroken(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	const total = 583
	srv := mockInformaticsRuns(t, total, nil)
	defer srv.Close()
	c := &InformaticsAPIClient{baseURL: srv.URL, httpClient: srv.Client(), loggedIn: true}

	runs, lost, err := c.collectNewInformaticsRuns(context.Background(), "207033", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lost != 0 {
		t.Fatalf("expected no losses, got %d", lost)
	}
	if len(runs) != total {
		t.Fatalf("expected all %d runs, got %d", total, len(runs))
	}
}
