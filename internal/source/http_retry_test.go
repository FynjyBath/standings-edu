package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRetriableHTTPStatus(t *testing.T) {
	retriable := []int{429, 500, 502, 503, 504}
	for _, code := range retriable {
		if !isRetriableHTTPStatus(code) {
			t.Errorf("status %d must be retriable", code)
		}
	}
	notRetriable := []int{200, 301, 400, 401, 403, 404}
	for _, code := range notRetriable {
		if isRetriableHTTPStatus(code) {
			t.Errorf("status %d must NOT be retriable", code)
		}
	}
}

// Временный 500 на первых попытках сменяется 200 — запрос должен успешно
// восстановиться, а не уронить аккаунт.
func TestDoHTTPWithRetryRecoversFrom500(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond // не тормозим тест
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	res, err := doHTTPWithRetry(srv.Client(), req, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after retries, got %d", res.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

// Если 500 не проходит за все попытки — возвращаем последний ответ, чтобы
// вызывающий сам оформил ошибку по коду (поведение как раньше).
func TestDoHTTPWithRetryExhaustsReturnsLastResponse(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	res, err := doHTTPWithRetry(srv.Client(), req, 3)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 passed through, got %d", res.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

// 4xx не повторяется — одна попытка.
func TestDoHTTPWithRetryDoesNotRetry4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	res, err := doHTTPWithRetry(srv.Client(), req, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer res.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("4xx must not be retried, got %d attempts", got)
	}
}

// End-to-end: временный 500 на странице runs у informatics восстанавливается
// повтором, аккаунт не выпадает (fetchRunsPage возвращает распарсенный ответ).
func TestInformaticsFetchRunsPageRetriesTransient500(t *testing.T) {
	httpRetryBaseDelay = time.Millisecond
	defer func() { httpRetryBaseDelay = 400 * time.Millisecond }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("<html>500</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"success","data":[{"id":42,"ejudge_status":0,"create_time":"2026-07-01T09:00:00+00:00","problem":{"id":7}}],"metadata":{"page_count":1,"count":1}}`))
	}))
	defer srv.Close()

	c := &InformaticsAPIClient{
		baseURL:    srv.URL,
		httpClient: srv.Client(),
		loggedIn:   true,
	}
	resp, err := c.fetchRunsPage(context.Background(), "123", 1)
	if err != nil {
		t.Fatalf("fetchRunsPage should recover, got error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != 42 {
		t.Fatalf("unexpected parsed data: %+v", resp.Data)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}
