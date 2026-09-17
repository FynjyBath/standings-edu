package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Строки прогресса пишет генератор, а разбирает админка — формат у них общий,
// иначе прогресс молча перестанет считаться.
func TestProgressLineRoundTrip(t *testing.T) {
	line := FormatProgress("accounts", 17, 210)
	stage, done, total, ok := parseProgressLine("2026/09/17 16:00:00 " + line)
	if !ok {
		t.Fatalf("строка не разобралась: %q", line)
	}
	if stage != "accounts" || done != 17 || total != 210 {
		t.Fatalf("разобралось как stage=%q done=%d total=%d", stage, done, total)
	}
}

// Строки прогресса не должны попадать в показанный вывод: их сотни.
func TestProgressWriterStripsProgressLines(t *testing.T) {
	var out bytes.Buffer
	seen := make([][3]any, 0)
	w := &progressWriter{out: &out, on: func(stage string, done, total int) {
		seen = append(seen, [3]any{stage, done, total})
	}}
	chunks := []string{
		"INFO начали\n2026/09/17 16:00:00 PROG",
		"RESS stage=accounts done=1 total=3\nINFO дальше\n",
		"PROGRESS stage=accounts done=3 total=3\nINFO конец",
	}
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()

	got := out.String()
	if strings.Contains(got, "PROGRESS") {
		t.Errorf("строки прогресса просочились в вывод: %q", got)
	}
	for _, want := range []string{"INFO начали", "INFO дальше", "INFO конец"} {
		if !strings.Contains(got, want) {
			t.Errorf("обычная строка потерялась: нет %q в %q", want, got)
		}
	}
	if len(seen) != 2 || seen[1] != [3]any{"accounts", 3, 3} {
		t.Errorf("прогресс разобран неверно: %+v", seen)
	}
}

// Прогресс, разрезанный посередине строки между двумя Write, должен склеиться.
func TestProgressWriterHandlesSplitLines(t *testing.T) {
	var out bytes.Buffer
	var last [3]any
	w := &progressWriter{out: &out, on: func(s string, d, tt int) { last = [3]any{s, d, tt} }}
	for _, c := range []string{"PROGRESS stage=gr", "oups done=7 tot", "al=42\n"} {
		_, _ = w.Write([]byte(c))
	}
	if last != [3]any{"groups", 7, 42} {
		t.Fatalf("склейка не сработала: %+v", last)
	}
	if out.Len() != 0 {
		t.Errorf("в вывод ничего не должно было попасть: %q", out.String())
	}
}

func TestProgressPercent(t *testing.T) {
	for _, c := range []struct {
		done, total, want int
	}{{0, 10, 0}, {5, 10, 50}, {10, 10, 100}, {12, 10, 100}, {3, 0, 0}} {
		p := AdminActionProgress{Done: c.done, Total: c.total}
		if got := p.Percent(); got != c.want {
			t.Errorf("%d/%d -> %d%%, ожидалось %d%%", c.done, c.total, got, c.want)
		}
	}
	if (AdminActionProgress{Total: 0}).Known() {
		t.Error("без общего числа показывать «столько из стольких» нечего")
	}
}

// История хранит несколько действий, новейшее сверху, и не растёт бесконечно.
func TestAdminHistoryKeepsRecentNewestFirst(t *testing.T) {
	h, _ := newTestHandlers(t)
	for i := 0; i < adminHistoryLimit+5; i++ {
		h.setAdminResult(newAdminResult(fmt.Sprintf("act%d", i), true, 0, time.Now(), "вывод", nil))
	}
	hist := h.adminHistory()
	if len(hist) != adminHistoryLimit {
		t.Fatalf("в истории %d записей, ожидалось %d", len(hist), adminHistoryLimit)
	}
	if hist[0].Action != fmt.Sprintf("act%d", adminHistoryLimit+4) {
		t.Errorf("сверху должно быть новейшее, а не %q", hist[0].Action)
	}
	if hist[len(hist)-1].Action != "act5" {
		t.Errorf("самые старые должны вытесняться, снизу %q", hist[len(hist)-1].Action)
	}
}

// Действие уходит в фон: обработчик возвращается сразу, не дожидаясь конца.
func TestStartAdminActionRunsInBackground(t *testing.T) {
	h, _ := newTestHandlers(t)
	release := make(chan struct{})
	done := make(chan struct{})
	started := time.Now()
	if !h.startAdminAction("slow", func() AdminActionResult {
		<-release
		close(done)
		return newAdminResult("slow", true, 0, time.Now(), "", nil)
	}) {
		t.Fatal("действие должно было запуститься")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("обработчик ждал завершения: %v", elapsed)
	}
	// Пока идёт — виден прогресс и второе действие не стартует.
	if p := h.adminProgress(); p == nil || p.Action != "slow" {
		t.Fatalf("во время работы должен быть виден прогресс: %+v", p)
	}
	if h.startAdminAction("other", func() AdminActionResult {
		t.Error("второе действие не должно было запуститься")
		return AdminActionResult{}
	}) {
		t.Error("параллельный запуск должен отклоняться")
	}
	close(release)
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.adminHistory()) > 0 && h.adminProgress() == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("после завершения ждали запись в истории и снятый прогресс: history=%d progress=%+v",
		len(h.adminHistory()), h.adminProgress())
}

// Занятый сервер отвечает редиректом с пометкой, а не молча проглатывает клик.
func TestAdminActionRedirectsWhenBusy(t *testing.T) {
	h, _ := newTestHandlers(t)
	release := make(chan struct{})
	defer close(release)
	if !h.startAdminAction("slow", func() AdminActionResult {
		<-release
		return newAdminResult("slow", true, 0, time.Now(), "", nil)
	}) {
		t.Fatal("первое действие должно было запуститься")
	}
	rec := httptest.NewRecorder()
	h.AdminActionGenerate(rec, httptest.NewRequest(http.MethodPost, "/standings/admin/actions/generate", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "busy=1") {
		t.Errorf("ожидали пометку занятости в %q", loc)
	}
}

// Состояние прогресса трогают из горутины команды и из обработчика страницы
// одновременно — гонок быть не должно (проверяется под -race).
func TestProgressStateConcurrent(t *testing.T) {
	var st adminProgressState
	st.start("generate")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for k := 0; k < 200; k++ {
				st.update("accounts", k, 200)
				_ = st.snapshot()
			}
		}(i)
	}
	wg.Wait()
	st.finish()
	if st.snapshot() != nil {
		t.Error("после завершения прогресса быть не должно")
	}
}
