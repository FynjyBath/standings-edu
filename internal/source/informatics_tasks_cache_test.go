package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func newTasksCacheClient(t *testing.T, refresh bool) (*InformaticsAPIClient, *int64, string) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/index.php" {
			w.Write([]byte(`name="logintoken" value="x"`))
			return
		}
		atomic.AddInt64(&hits, 1)
		w.Write([]byte(`<title>Задача №111. Кэшируемая</title>
<div class="statements_toc statements_toc_alpha clearfix"><ul>
<li><a href="view.php?id=9&amp;chapterid=111"><b>A.</b> Кэшируемая</a></li>
</ul></div>`))
	}))
	t.Cleanup(srv.Close)

	c, err := NewInformaticsAPIClientWithState(InformaticsCredentials{Username: "u", Password: "p"}, "")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL
	c.loggedIn = true // не гоняем реальный логин-флоу
	c.lastLogin = time.Now()
	cachePath := filepath.Join(t.TempDir(), "tasks_cache.json")
	c.ConfigureTasksCache(cachePath, refresh)
	return c, &hits, cachePath
}

// Оглавление сборника и название задачи кэшируются: повторные вызовы не ходят
// в сеть; новый клиент с тем же файлом кэша тоже не ходит.
func TestInformaticsTasksCache(t *testing.T) {
	c, hits, cachePath := newTasksCacheClient(t, false)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		problems, err := c.FetchStatementProblems(ctx, 9)
		if err != nil || len(problems) != 1 || problems[0].Title != "A. Кэшируемая" {
			t.Fatalf("statement: %v %+v", err, problems)
		}
	}
	if *hits != 1 {
		t.Fatalf("сборник должен качаться один раз, hits=%d", *hits)
	}

	for i := 0; i < 3; i++ {
		title, err := c.FetchTaskTitle(ctx, 111)
		if err != nil || title != "Кэшируемая" {
			t.Fatalf("title: %v %q", err, title)
		}
	}
	if *hits != 2 {
		t.Fatalf("название должно качаться один раз, hits=%d", *hits)
	}

	// Новый клиент (новый процесс) с тем же файлом — берёт с диска, сеть не нужна.
	c2, err := NewInformaticsAPIClientWithState(InformaticsCredentials{Username: "u", Password: "p"}, "")
	if err != nil {
		t.Fatal(err)
	}
	c2.baseURL = "http://127.0.0.1:1" // сеть недоступна
	c2.ConfigureTasksCache(cachePath, false)
	problems, err := c2.FetchStatementProblems(ctx, 9)
	if err != nil || len(problems) != 1 {
		t.Fatalf("дисковый кэш должен работать без сети: %v", err)
	}
	if title, err := c2.FetchTaskTitle(ctx, 111); err != nil || title != "Кэшируемая" {
		t.Fatalf("дисковый кэш названий должен работать без сети: %v %q", err, title)
	}
}

// refresh=true перечитывает с сайта, минуя кэш (но кэш обновляет).
func TestInformaticsTasksCacheRefresh(t *testing.T) {
	c, hits, _ := newTasksCacheClient(t, true)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := c.FetchStatementProblems(ctx, 9); err != nil {
			t.Fatal(err)
		}
	}
	if *hits != 2 {
		t.Fatalf("refresh должен ходить в сеть каждый раз, hits=%d", *hits)
	}
}
