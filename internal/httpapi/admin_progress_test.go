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
	line := FormatProgress("tables", 17, 210, "smip_2026_p4")
	stage, done, total, note, ok := parseProgressLine("2026/09/17 16:00:00 " + line)
	if !ok {
		t.Fatalf("строка не разобралась: %q", line)
	}
	if stage != "tables" || done != 17 || total != 210 || note != "smip_2026_p4" {
		t.Fatalf("разобралось как stage=%q done=%d total=%d note=%q", stage, done, total, note)
	}
	// Пометка необязательна — этапы без неё тоже должны разбираться.
	if _, _, _, note, ok := parseProgressLine(FormatProgress("profiles", 0, 0, "")); !ok || note != "" {
		t.Fatalf("этап без пометки: ok=%v note=%q", ok, note)
	}
}

// Строки прогресса не должны попадать в показанный вывод: их сотни.
func TestProgressWriterStripsProgressLines(t *testing.T) {
	var out bytes.Buffer
	seen := make([][3]any, 0)
	w := &progressWriter{out: &out, on: func(stage string, done, total int, _ string) {
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
	w := &progressWriter{out: &out, on: func(s string, d, tt int, _ string) { last = [3]any{s, d, tt} }}
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
				st.update("accounts", k, 200, "acmp")
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

// Смена этапа сбрасывает счётчик: иначе полоса застывала бы на «165 из 165,
// 100%» всё время, пока идёт следующий, ещё не отчитавшийся этап, — и читалась
// бы как «готово, но висит».
func TestProgressStageChangeResetsCount(t *testing.T) {
	var st adminProgressState
	st.start("generate")
	st.update("accounts", 165, 165, "informatics")
	if p := st.snapshot(); p.Percent() != 100 {
		t.Fatalf("конец этапа должен показывать 100%%, показывает %d", p.Percent())
	}
	st.update("tables", 0, 42, "all_ku")
	p := st.snapshot()
	if p.Stage != "tables" || p.Done != 0 || p.Total != 42 || p.Percent() != 0 {
		t.Fatalf("новый этап должен начинаться с нуля: %+v (%d%%)", p, p.Percent())
	}
	if p.Note != "all_ku" {
		t.Errorf("пометка должна показывать, что обрабатывается: %q", p.Note)
	}
}

// Застывший прогресс должен отличаться от только что сдвинувшегося: без этого
// «165 из 165» одинаково выглядит и через секунду, и через десять минут.
func TestProgressStallDetection(t *testing.T) {
	fresh := AdminActionProgress{UpdatedAt: time.Now()}
	if fresh.Stalled() {
		t.Error("только что обновлённый прогресс не застыл")
	}
	old := AdminActionProgress{UpdatedAt: time.Now().Add(-stalledAfter - time.Minute)}
	if !old.Stalled() {
		t.Error("давно не двигавшийся прогресс должен помечаться застывшим")
	}
	if got := old.StalledFor(); got == "" || strings.HasPrefix(got, "-") {
		t.Errorf("время простоя должно быть положительным, получили %q", got)
	}
}

// Каждый этап генерации должен иметь человеческое название: иначе в полосе
// появится сырой идентификатор из кода.
func TestEveryStageHasTitle(t *testing.T) {
	for _, stage := range []string{"accounts", "tables", "profiles", "tempo", "review", "write"} {
		p := AdminActionProgress{Stage: stage}
		title := p.StageTitle()
		if title == "" || title == "выполняется" {
			t.Errorf("этап %q без названия: %q", stage, title)
		}
	}
}
