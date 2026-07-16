package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"standings-edu/internal/domain"
)

// Кэш готового HTML публичной страницы группы. Рендер большой страницы стоит
// сотни миллисекунд CPU на каждый запрос — кэшируем результат и инвалидируем
// по отпечатку файлов (standings, group.json, глобальное «последнее
// обновление») и по ближайшей временной границе (старт контеста меняет вид
// страницы: до него сервер прячет ссылки на задачи).
//
// Кэшируется ТОЛЬКО публичный вид (без ?token=…): токенные страницы рендерятся
// как раньше. В закэшированном HTML на отдаче подставляется актуальное
// data-server-now (по нему клиент синхронизирует часы для отсчётов).
type groupPageCache struct {
	mu      sync.Mutex
	entries map[string]groupPageCacheEntry
}

type groupPageCacheEntry struct {
	version   string
	expiresAt time.Time // нулевое — без временной границы
	html      []byte
}

var serverNowRe = regexp.MustCompile(`data-server-now="[^"]*"`)

// groupPageVersion — отпечаток входов страницы группы. ok=false — страницу не
// кэшируем (объединённая группа, отсутствующие файлы и т.п.).
func (h *Handlers) groupPageVersion(slug string) (string, bool) {
	if h.dataDir == "" || !domain.IsValidSlug(slug) {
		return "", false
	}
	// Объединённые группы собираются из файлов участниц — их отпечаток сложнее,
	// проще не кэшировать (их немного и они меняются вместе с участницами).
	if gf, ok := h.readSourceGroupFile(slug); ok && len(gf.MemberGroups) > 0 {
		return "", false
	}
	var b bytes.Buffer
	for _, p := range []string{
		filepath.Join(h.loader.OutDir, "standings", slug+".json"),
		filepath.Join(h.dataDir, "groups", slug, "group.json"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			return "", false
		}
		fmt.Fprintf(&b, "%d|%d;", info.Size(), info.ModTime().UnixNano())
	}
	// Глобальная метка генерации — футер («последнее обновление») зависит от неё.
	if t, err := h.loader.LoadLastUpdatedAt(); err == nil {
		fmt.Fprintf(&b, "u%d;", t.UnixNano())
	}
	return b.String(), true
}

// nextTimeBoundary — ближайший будущий момент, когда серверный вид страницы
// изменится сам по себе (старт контеста открывает ссылки на задачи).
func nextTimeBoundary(standings *domain.GeneratedGroupStandings, now time.Time) time.Time {
	var next time.Time
	for i := range standings.Contests {
		st := standings.Contests[i].StartTime
		if st != nil && st.After(now) && (next.IsZero() || st.Before(next)) {
			next = *st
		}
	}
	return next
}

func (h *Handlers) cachedGroupPage(slug, version string) ([]byte, bool) {
	h.pageCache.mu.Lock()
	defer h.pageCache.mu.Unlock()
	e, ok := h.pageCache.entries[slug]
	if !ok || e.version != version {
		return nil, false
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		delete(h.pageCache.entries, slug)
		return nil, false
	}
	return e.html, true
}

func (h *Handlers) storeGroupPage(slug, version string, expiresAt time.Time, html []byte) {
	h.pageCache.mu.Lock()
	defer h.pageCache.mu.Unlock()
	if h.pageCache.entries == nil {
		h.pageCache.entries = make(map[string]groupPageCacheEntry)
	}
	h.pageCache.entries[slug] = groupPageCacheEntry{version: version, expiresAt: expiresAt, html: html}
}

// writeCachedHTML отдаёт закэшированный HTML, подставив актуальное серверное
// время (для клиентской синхронизации часов).
func writeCachedHTML(w http.ResponseWriter, html []byte) {
	now := time.Now().Format(time.RFC3339)
	patched := serverNowRe.ReplaceAll(html, []byte(`data-server-now="`+now+`"`))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(patched)
}
