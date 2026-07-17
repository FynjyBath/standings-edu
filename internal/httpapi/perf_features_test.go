package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

// Хендлеры с общим каталогом data+generated (для страницы группы: кэш HTML
// смотрит и на data/groups/<slug>/group.json, и на generated/standings).
func newPerfTestHandlers(t *testing.T) (*Handlers, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	outDir := t.TempDir()
	h := NewHandlers(
		storage.NewGeneratedLoader(outDir),
		nil,
		web.NewTemplateRenderer(filepath.Join("..", "..", "web", "templates")),
		log.New(io.Discard, "", 0),
	)
	h.ConfigureSourceDir(dataDir)
	return h, dataDir, outDir
}

func writePerfGroup(t *testing.T, dataDir, outDir, slug string, nContests int) {
	t.Helper()
	writeTestFile(t, filepath.Join(dataDir, "groups", slug, "group.json"), `{"title":"Группа","student_ids":["s1"]}`)
	std := domain.GeneratedGroupStandings{GroupSlug: slug, GroupTitle: "Группа"}
	for c := 0; c < nContests; c++ {
		std.Contests = append(std.Contests, domain.GeneratedContestStandings{
			ID: fmt.Sprintf("c%d", c), Title: fmt.Sprintf("Контест %d", c), ScoreSystem: domain.ScoreSystemEdu,
			Tasks:       []domain.GeneratedTask{{Label: "A", URL: "https://e.com/a", NormalizedURL: "e/a"}},
			Subcontests: []domain.GeneratedSubcontest{{Title: "T", TaskCount: 1, Tasks: []domain.GeneratedTask{{Label: "A", URL: "https://e.com/a"}}}},
			Rows:        []domain.GeneratedRow{{StudentID: "s1", PublicName: "Иван", Place: "1", SolvedCount: 1, Statuses: []string{"solved"}}},
		})
	}
	b, _ := json.Marshal(std)
	writeTestFile(t, filepath.Join(outDir, "standings", slug+".json"), string(b))
}

func getPage(t *testing.T, h *Handlers, url string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	slug := strings.Split(strings.TrimPrefix(url, "/standings/"), "/")[0]
	slug = strings.Split(slug, "?")[0]
	req.SetPathValue("group_name", slug)
	rec := httptest.NewRecorder()
	// маршрутизация вручную по пути
	switch {
	case strings.Contains(url, "/contest-fragment"):
		h.GroupContestFragment(rec, req)
	case strings.Contains(url, "/summary-data"):
		h.GroupSummaryData(rec, req)
	default:
		h.GroupStandingsPage(rec, req)
	}
	return rec.Code, rec.Body.String()
}

// Публичная страница с >10 контестами: первые 8 полные, остальные — ленивые
// заглушки; с токеном — всё полное (поведение жюри не меняется).
func TestLazyContestPlaceholders(t *testing.T) {
	h, dataDir, outDir := newPerfTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "groups", "g", "group.json"), `{"title":"Г","student_ids":["s1"],"group_secret_token":"TOK"}`)
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г"}
	for c := 0; c < 12; c++ {
		std.Contests = append(std.Contests, domain.GeneratedContestStandings{
			ID: fmt.Sprintf("c%d", c), Title: fmt.Sprintf("Контест %d", c), ScoreSystem: domain.ScoreSystemEdu,
			Rows: []domain.GeneratedRow{{StudentID: "s1", PublicName: "Иван", SolvedCount: 0, Statuses: []string{}}},
		})
	}
	b, _ := json.Marshal(std)
	writeTestFile(t, filepath.Join(outDir, "standings", "g.json"), string(b))

	code, body := getPage(t, h, "/standings/g")
	if code != 200 {
		t.Fatalf("public page: %d", code)
	}
	if got := strings.Count(body, `data-lazy-id="`); got != 4 {
		t.Fatalf("публичная страница с 12 контестами должна содержать 4 ленивые заглушки, got %d", got)
	}
	if !strings.Contains(body, `data-lazy-id="c11"`) {
		t.Fatal("последний контест должен быть заглушкой")
	}
	if strings.Contains(body, `data-lazy-id="c0"`) {
		t.Fatal("первые контесты должны быть полными")
	}
	// Заглушка содержит заголовок (для оглавления) и якорный id.
	if !strings.Contains(body, `id="contest-c11"`) || !strings.Contains(body, "Контест 11") {
		t.Fatal("заглушка должна нести id и заголовок")
	}

	// С токеном — без заглушек.
	code, body = getPage(t, h, "/standings/g?token=TOK")
	if code != 200 || strings.Contains(body, `data-lazy-id="`) {
		t.Fatalf("токенная страница должна быть полной (code=%d, lazy=%v)", code, strings.Contains(body, `data-lazy-id="`))
	}
}

// Фрагмент контеста: отдаёт таблицу нужного контеста, 404 на неизвестный id.
func TestContestFragment(t *testing.T) {
	h, dataDir, outDir := newPerfTestHandlers(t)
	writePerfGroup(t, dataDir, outDir, "g", 12)

	code, body := getPage(t, h, "/standings/g/contest-fragment?id=c11")
	if code != 200 {
		t.Fatalf("fragment: %d", code)
	}
	if !strings.Contains(body, "Контест 11") || !strings.Contains(body, "standings-table") {
		t.Fatalf("фрагмент должен содержать таблицу контеста: %.200s", body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "site-header") {
		t.Fatal("фрагмент не должен быть обёрнут в layout")
	}
	if code, _ := getPage(t, h, "/standings/g/contest-fragment?id=nope"); code != 404 {
		t.Fatalf("unknown id must 404, got %d", code)
	}
}

// Кэш HTML публичной страницы: повторный запрос отдаётся из кэша с обновлённым
// data-server-now; изменение standings-файла инвалидирует кэш.
func TestGroupPageHTMLCache(t *testing.T) {
	h, dataDir, outDir := newPerfTestHandlers(t)
	writePerfGroup(t, dataDir, outDir, "g", 3)

	_, body1 := getPage(t, h, "/standings/g")
	if len(h.pageCache.entries) != 1 {
		t.Fatalf("после первого запроса страница должна закэшироваться: %d", len(h.pageCache.entries))
	}
	// Подменяем кэш меткой, чтобы доказать, что второй ответ пришёл из кэша.
	for k, e := range h.pageCache.entries {
		e.html = []byte(strings.Replace(string(e.html), "Группа", "Группа-ИЗ-КЭША", 1))
		h.pageCache.entries[k] = e
	}
	_, body2 := getPage(t, h, "/standings/g")
	if !strings.Contains(body2, "Группа-ИЗ-КЭША") {
		t.Fatal("второй запрос должен отдаться из кэша")
	}
	// server-now в кэшированном ответе — актуальный (не старее секунды).
	i := strings.Index(body2, `data-server-now="`)
	if i < 0 {
		t.Fatal("нет data-server-now")
	}
	v := body2[i+len(`data-server-now="`):]
	v = v[:strings.Index(v, `"`)]
	ts, err := time.Parse(time.RFC3339, v)
	if err != nil || time.Since(ts) > 5*time.Second {
		t.Fatalf("server-now должен быть свежим: %q err=%v", v, err)
	}

	// Токенный запрос — мимо кэша (метки нет).
	writeTestFile(t, filepath.Join(dataDir, "groups", "g", "group.json"), `{"title":"Группа","student_ids":["s1"],"group_secret_token":"TOK"}`)
	_, body3 := getPage(t, h, "/standings/g?token=TOK")
	if strings.Contains(body3, "ИЗ-КЭША") {
		t.Fatal("токенный запрос не должен идти через кэш")
	}

	// Изменение standings-файла (mtime/size) инвалидирует кэш.
	time.Sleep(10 * time.Millisecond)
	writePerfGroup(t, dataDir, outDir, "g", 4)
	_, body4 := getPage(t, h, "/standings/g")
	if strings.Contains(body4, "ИЗ-КЭША") {
		t.Fatal("после перегенерации кэш должен инвалидироваться")
	}
	_ = body1
	_ = os.Getenv
}

// summary-data отдаёт JSON с теми же правилами видимости; страница summary
// больше не содержит встроенного JSON.
func TestSummaryDataEndpoint(t *testing.T) {
	h, dataDir, outDir := newPerfTestHandlers(t)
	writePerfGroup(t, dataDir, outDir, "g", 2)

	code, body := getPage(t, h, "/standings/g/summary-data")
	if code != 200 {
		t.Fatalf("summary-data: %d", code)
	}
	var std domain.GeneratedGroupStandings
	if err := json.Unmarshal([]byte(body), &std); err != nil || len(std.Contests) != 2 {
		t.Fatalf("summary-data должен отдавать standings JSON: err=%v contests=%d", err, len(std.Contests))
	}

	req := httptest.NewRequest(http.MethodGet, "/standings/g/summary", nil)
	req.SetPathValue("group_name", "g")
	rec := httptest.NewRecorder()
	h.GroupSummaryAllPage(rec, req)
	page := rec.Body.String()
	if strings.Contains(page, `id="summary-data"`) {
		t.Fatal("страница сводной не должна встраивать JSON")
	}
	if !strings.Contains(page, "/summary-data") {
		t.Fatal("страница сводной должна грузить данные с /summary-data")
	}
}

// summary-data: компактный JSON (без отступов), gzip по Accept-Encoding и кэш
// готовых байтов с инвалидацией по отпечатку файлов.
func TestSummaryDataGzipAndCache(t *testing.T) {
	h, dataDir, outDir := newPerfTestHandlers(t)
	writePerfGroup(t, dataDir, outDir, "g", 2)

	get := func(gzipHdr bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/standings/g/summary-data", nil)
		req.SetPathValue("group_name", "g")
		if gzipHdr {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		rec := httptest.NewRecorder()
		h.GroupSummaryData(rec, req)
		return rec
	}

	plain := get(false)
	if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("plain: code=%d enc=%q", plain.Code, plain.Header().Get("Content-Encoding"))
	}
	if strings.Contains(plain.Body.String(), "\n  ") {
		t.Fatal("JSON должен быть компактным, без отступов")
	}

	zipped := get(true)
	if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gzip-ответ: enc=%q vary=%q", zipped.Header().Get("Content-Encoding"), zipped.Header().Get("Vary"))
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	unzipped, err := io.ReadAll(zr)
	if err != nil || string(unzipped) != plain.Body.String() {
		t.Fatalf("gzip должен разворачиваться в тот же JSON: err=%v", err)
	}
	if zipped.Body.Len() >= plain.Body.Len() {
		t.Fatalf("gzip должен быть меньше: %d >= %d", zipped.Body.Len(), plain.Body.Len())
	}

	// Кэш: повторный запрос отдаёт байты из кэша (запись появилась).
	if _, ok := h.cachedSummaryData("g|false", func() string { v, _ := h.groupPageVersion("g"); return v }()); !ok {
		t.Fatal("после первого запроса ответ должен закэшироваться")
	}
	// Изменение standings-файла инвалидирует кэш: новые данные видны.
	writePerfGroup(t, dataDir, outDir, "g", 3)
	var std domain.GeneratedGroupStandings
	if err := json.Unmarshal([]byte(get(false).Body.String()), &std); err != nil || len(std.Contests) != 3 {
		t.Fatalf("после изменения файла кэш должен обновиться: err=%v contests=%d", err, len(std.Contests))
	}
}
