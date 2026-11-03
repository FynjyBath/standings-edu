package httpapi

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

func shortGet(t *testing.T, h *Handlers, short, remote, forwarded string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, short, nil)
	req.SetPathValue("code", strings.TrimPrefix(short, shortLinkPrefix))
	if remote != "" {
		req.RemoteAddr = remote
	}
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	rec := httptest.NewRecorder()
	h.ShortLinkRedirect(rec, req)
	return rec
}

func TestRandomShortCode(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 2000; i++ {
		code, err := randomShortCode()
		if err != nil {
			t.Fatal(err)
		}
		if !validShortCode(code) {
			t.Fatalf("код %q вне алфавита или не той длины", code)
		}
		if strings.ContainsAny(code, "0O1lIo") {
			t.Fatalf("в коде %q похожие символы", code)
		}
		seen[code] = struct{}{}
	}
	if len(seen) < 1990 {
		t.Errorf("слишком много совпадений: %d разных из 2000", len(seen))
	}
}

// Короткая ссылка постоянна: тот же адрес — тот же код, и после перезапуска
// сервера (новый экземпляр читает файл). Чужие адреса не принимаются.
func TestShortLinksArePersistentAndInternal(t *testing.T) {
	h, dataDir := childTestSetup(t)
	a := h.shortLink("/standings/g1")
	b := h.shortLink("/standings/g1?token=PLTOK")
	if !strings.HasPrefix(a, "/s/") || len(a) != len("/s/")+shortCodeLen || a == b {
		t.Fatalf("короткие ссылки: %q, %q", a, b)
	}
	if again := h.shortLink("/standings/g1"); again != a {
		t.Errorf("повторный запрос дал другой код: %q → %q", a, again)
	}

	path := filepath.Join(dataDir, "credentials", "short_links.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("в файле адреса с токенами — права должны быть 0600, а не %v", info.Mode().Perm())
	}

	// «Перезапуск»: новый сервер поверх тех же данных.
	h2 := NewHandlers(storage.NewGeneratedLoader(t.TempDir()), nil,
		web.NewTemplateRenderer(filepath.Join("..", "..", "web", "templates")), log.New(io.Discard, "", 0))
	h2.ConfigureSourceDir(dataDir)
	if got := h2.shortLink("/standings/g1"); got != a {
		t.Errorf("после перезапуска код сменился: %q → %q", a, got)
	}

	for _, bad := range []string{"https://evil.example/standings", "//evil.example/standings/g1", "/admin", ""} {
		if got := h.shortLink(bad); got != "" {
			t.Errorf("для %q короткая ссылка заводиться не должна, получено %q", bad, got)
		}
	}
}

func TestShortLinkRedirect(t *testing.T) {
	h, _ := childTestSetup(t)
	short := h.shortLink("/standings/g1?token=PLTOK")

	rec := shortGet(t, h, short, "", "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/standings/g1?token=PLTOK" {
		t.Fatalf("редирект: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("за короткой ссылкой токен: нужны no-referrer и no-store")
	}
	for _, bad := range []string{"/s/AAAAAA", "/s/abc", "/s/0O1lIo", "/s/"} {
		if code := shortGet(t, h, bad, "", "").Code; code != http.StatusNotFound {
			t.Errorf("%s: code=%d, want 404", bad, code)
		}
	}
}

// Перебор кодов: после shortMissLimit промахов адрес получает 429 на любые
// короткие ссылки, включая верные; другие адреса работают. Адрес — последний
// в X-Forwarded-For: первый клиент подставляет сам.
func TestShortLinkMissLimit(t *testing.T) {
	h, _ := childTestSetup(t)
	short := h.shortLink("/standings/g1")

	for i := 0; i < shortMissLimit; i++ {
		// Первый адрес в X-Forwarded-For меняется — лимит его не замечает.
		spoofed := "10.0.0." + string(rune('a'+i%26)) + ", 203.0.113.7"
		if code := shortGet(t, h, "/s/AAAAAA", "192.0.2.1:1234", spoofed).Code; code != http.StatusNotFound {
			t.Fatalf("промах %d: code=%d", i, code)
		}
	}
	if code := shortGet(t, h, short, "192.0.2.1:1234", "1.2.3.4, 203.0.113.7").Code; code != http.StatusTooManyRequests {
		t.Errorf("после %d промахов адрес должен получать 429 и на верный код, code=%d", shortMissLimit, code)
	}
	if code := shortGet(t, h, short, "192.0.2.1:1234", "203.0.113.8").Code; code != http.StatusFound {
		t.Errorf("другой адрес не должен страдать, code=%d", code)
	}
}

// При старте короткие ссылки появляются у всех существующих ссылок на таблицы.
// Смена токена: у новой ссылки свой код, а старый ведёт на отменённый токен.
func TestEnsureShortLinksCoversExistingLinks(t *testing.T) {
	h, dataDir := childTestSetup(t)
	writeTestFile(t, filepath.Join(dataDir, "credentials", "global_accesses.json"),
		`[{"id":"gl","title":"Куратор","auth":"token","token":"GLTOK","scope":"all","perms":["view.directory"]}]`)

	created := h.EnsureShortLinks()
	if created == 0 {
		t.Fatal("ссылки должны были завестись")
	}
	byCode := map[string]string{}
	blob, err := os.ReadFile(filepath.Join(dataDir, "credentials", "short_links.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, &byCode); err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{}
	for _, target := range byCode {
		targets[target] = true
	}
	for _, want := range []string{
		"/standings/g1", "/standings/g2", "/standings/g3", "/standings/g4",
		"/standings/g1?token=PLTOK", "/standings/g1?token=VPTOK", "/standings/g1?token=PPTOK",
		"/standings?token=GLTOK",
		childLink(t, "s1"), childLink(t, "s2"),
	} {
		if !targets[want] {
			t.Errorf("нет короткой ссылки для %s", want)
		}
	}
	if again := h.EnsureShortLinks(); again != 0 {
		t.Errorf("повторный старт не должен заводить новых, завёл %d", again)
	}

	oldShort := h.shortLink("/standings/g1?token=PLTOK")
	writeTestFile(t, filepath.Join(dataDir, "groups", "g1", "group.json"), `{"title":"Альфа","student_ids":["s1","s2"],
		"accesses":[{"id":"pl","title":"Родители","auth":"token","token":"NEWTOK","perms":["view.parent_links"]}]}`)
	h.EnsureShortLinks()
	newShort := h.shortLink("/standings/g1?token=NEWTOK")
	if newShort == "" || newShort == oldShort {
		t.Errorf("у ссылки с новым токеном должен быть свой код: %q / %q", oldShort, newShort)
	}
	if loc := shortGet(t, h, oldShort, "", "").Header().Get("Location"); loc != "/standings/g1?token=PLTOK" {
		t.Errorf("старый код должен вести на старый адрес (с уже отменённым токеном), ведёт на %q", loc)
	}
}

// Короткие ссылки видны рядом с обычными: каталог, редактор доступов, ссылки
// для родителей, профиль, админка группы.
func TestShortLinksShownInInterface(t *testing.T) {
	h, dataDir := childTestSetup(t)
	writeTestFile(t, filepath.Join(dataDir, "credentials", "global_accesses.json"),
		`[{"id":"gl","title":"Куратор","auth":"token","token":"GLTOK","scope":"all","perms":["view.directory","view.participants","view.parent_links"]}]`)

	get := func(handler http.HandlerFunc, target, slug string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if slug != "" {
			req.SetPathValue("group_name", slug)
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", target, rec.Code)
		}
		return rec.Body.String()
	}
	hasShort := func(where, body, target string) {
		t.Helper()
		short := h.shortLink(target)
		if short == "" || !strings.Contains(body, `class="short-link" href="`+short+`"`) {
			t.Errorf("%s: нет короткой ссылки %q для %s", where, short, target)
		}
	}

	dir := get(h.IndexPage, "/standings?token=GLTOK", "")
	hasShort("каталог", dir, "/standings/g1")
	hasShort("каталог", dir, "/standings/g1?token=PLTOK")

	links := get(h.GroupParentLinksPage, "/standings/g1/manage/parent-links?token=PLTOK", "g1")
	hasShort("ссылки для родителей", links, childLink(t, "s1"))

	profile := get(h.GroupStudentProfilePage, "/standings/g1/student?id=s1&token=PPTOK", "g1")
	hasShort("профиль", profile, childLink(t, "s1"))

	adminGroup := get(h.AdminGroupManagePage, "/standings/admin/group?slug=g1", "")
	hasShort("админка группы", adminGroup, "/standings/g1")

	adminMain := get(h.AdminPage, "/standings/admin", "")
	hasShort("главная админки", adminMain, "/standings/g1")

	// Редактор доступов: короткая ссылка приходит в данных формы.
	editor := h.buildAccessEditor(false, "g1", "/x", h.groupAccesses("g1"))
	var entries []map[string]any
	if err := json.Unmarshal([]byte(editor.AccessesJSON), &entries); err != nil {
		t.Fatal(err)
	}
	want := h.shortLink("/standings/g1?token=PLTOK")
	found := false
	for _, e := range entries {
		if e["token"] == "PLTOK" && e["short_url"] == want {
			found = true
		}
	}
	if !found {
		t.Errorf("в редакторе доступов нет короткой ссылки %q: %v", want, entries)
	}
	global := h.buildAccessEditor(true, "", "/x", h.loadGlobalAccesses())
	if !strings.Contains(string(global.AccessesJSON), h.shortLink("/standings?token=GLTOK")) {
		t.Error("у глобального доступа нет короткой ссылки на каталог")
	}
}

// Маршрут /s/{code} не должен пересекаться с остальными: на конфликте роутер
// Go падает при старте, а юнит-тесты хендлеров роутер не строят.
func TestRouterBuildsWithShortLinks(t *testing.T) {
	h, _ := childTestSetup(t)
	router := NewRouter(h, t.TempDir())
	short := h.shortLink("/standings/g1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, short, nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/standings/g1" {
		t.Errorf("через роутер: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}
