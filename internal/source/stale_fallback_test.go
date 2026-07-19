package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Недоступность informatics не затирает посылки: при ошибке забора клиент
// отдаёт результаты из сохранённого состояния, а не ошибку (иначе builder
// пропустил бы аккаунт и таблицы перегенерировались бы с нулями).
func TestInformaticsStaleFallbackOnFetchError(t *testing.T) {
	runs := []map[string]any{
		mkRun(200, 7, 0, "2026-07-01T10:05:00+00:00"), // OK
		mkRun(100, 8, 5, "2026-07-01T10:00:00+00:00"), // WA
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

	// Забор 1: сайт жив, состояние сохраняется.
	res, err := c.FetchUserResults(context.Background(), "77")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("ожидались 2 задачи: %+v", res)
	}

	// Сайт «упал»: сервер закрыт — соединение рвётся.
	srv.Close()

	res2, err := c.FetchUserResults(context.Background(), "77")
	if err != nil {
		t.Fatalf("при недоступности должны отдаваться кэшированные посылки, а не ошибка: %v", err)
	}
	if len(res2) != 2 {
		t.Fatalf("кэш должен вернуть те же 2 задачи: %+v", res2)
	}
	solved := 0
	for _, r := range res2 {
		if r.Solved {
			solved++
		}
	}
	if solved != 1 {
		t.Fatalf("в кэше одна решённая: %+v", res2)
	}

	// Новый аккаунт без состояния — ошибка как раньше (пустоту не выдумываем).
	if _, err := c.FetchUserResults(context.Background(), "88"); err == nil {
		t.Fatal("без состояния недоступность должна оставаться ошибкой")
	}

	// Отмена генерации — не сбой сайта: пробрасывается, кэш не подставляется.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.FetchUserResults(cancelled, "77"); err == nil {
		t.Fatal("отмена контекста должна возвращать ошибку")
	}
}

// То же для codeforces: при ошибке API отдаются сохранённые результаты.
func TestCodeforcesStaleFallbackOnFetchError(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "cf.json")
	now := time.Now().UTC()
	accounts := map[string]codeforcesAccountState{
		"tourist": {
			MaxSubmissionID: 5,
			Results:         []TaskResult{{TaskURL: "https://codeforces.com/problemset/problem/1/A", Solved: true, Timed: []TimedSubmission{{At: now, Solved: true}}}},
			UpdatedAt:       now,
		},
	}
	blob, _ := json.Marshal(codeforcesStateFile{Version: codeforcesStateVersion, Accounts: accounts})
	if err := os.WriteFile(statePath, blob, 0o644); err != nil {
		t.Fatal(err)
	}

	// Сервер отдаёт 200 с мусором — мгновенная ошибка разбора без ретраев.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := NewCodeforcesAPIClientWithState(statePath)
	c.baseURL = srv.URL

	res, err := c.FetchUserResults(context.Background(), "tourist")
	if err != nil {
		t.Fatalf("при недоступности должны отдаваться кэшированные посылки: %v", err)
	}
	if len(res) != 1 || !res[0].Solved {
		t.Fatalf("кэш должен вернуть сохранённый результат: %+v", res)
	}

	// Без состояния — ошибка как раньше.
	if _, err := c.FetchUserResults(context.Background(), "petr"); err == nil {
		t.Fatal("без состояния недоступность должна оставаться ошибкой")
	}
}
