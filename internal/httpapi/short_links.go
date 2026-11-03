package httpapi

// Короткие ссылки: /s/<6 символов> → редирект на полную ссылку на таблицы.
//
// Короткая ссылка есть у ссылок, которые интерфейс показывает, чтобы их
// раздали, — но только у тех, по которым НЕЛЬЗЯ НИЧЕГО МЕНЯТЬ: адрес группы,
// страницы учеников для родителей и ссылки доступов с токеном, чьи права
// только на просмотр (domain.ReadOnlyPerms). У доступа, который что-то меняет
// (оценки, кондуиты, состав, контесты…), короткой ссылки нет: шесть символов
// защищают слабее токена, и такой ключ к правке им не доверяем. Проверка идёт
// и при переходе: дали доступу права на изменение — его короткая ссылка сразу
// перестаёт работать, а при старте сервера вычищается из файла.
//
// Код случайный и привязан к ПОЛНОМУ адресу,
// а не к доступу: сменили токен — у нового адреса свой код, а старый ведёт на
// адрес с отменённым токеном, то есть на публичный вид. Так короткая ссылка
// никогда не переживает отзыв доступа.
//
// Коды создаются сами: при старте сервера — для всех существующих ссылок, и
// дальше при первом показе новой. Хранятся в data/credentials/short_links.json
// (код → адрес): там лежат и адреса с токенами, поэтому файл — среди секретов,
// в бандл экспорта не попадает.
//
// Шесть символов — это 56^6 ≈ 3·10^10 вариантов против 2^128 у токена, поэтому
// промахи по кодам ограничены по адресу клиента (shortMissLimit): перебирать
// коды с одного адреса бессмысленно.

import (
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

const (
	// shortLinkPrefix — путь коротких ссылок. Отдельный корень, а не
	// /standings/s/…: такой шаблон пересёкся бы с /standings/{group}/panel и
	// прочими, и роутер упал бы при старте.
	shortLinkPrefix = "/s/"
	shortCodeLen    = 6
	// shortCodeAlphabet — без похожих символов (0/O/o, 1/l/I): код иногда
	// перепечатывают с экрана или диктуют.
	shortCodeAlphabet = "23456789abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ"

	// Промахи по кодам с одного адреса: не больше shortMissLimit за
	// shortMissWindow, дальше адрес получает 429 до конца окна.
	shortMissLimit  = 30
	shortMissWindow = 10 * time.Minute
)

// shortLinkStore — коды и обратный индекс «адрес → код».
type shortLinkStore struct {
	mu       sync.Mutex
	loaded   bool
	byCode   map[string]string
	byTarget map[string]string

	missMu sync.Mutex
	misses map[string]shortMissCounter
}

type shortMissCounter struct {
	since time.Time
	count int
}

func (h *Handlers) shortLinksPath() string {
	if h.dataDir == "" {
		return ""
	}
	return filepath.Join(h.dataDir, "credentials", "short_links.json")
}

// loadShortLinksLocked читает файл один раз: пишет его только сервер.
func (h *Handlers) loadShortLinksLocked() error {
	s := &h.shortStore
	if s.loaded {
		return nil
	}
	s.byCode = map[string]string{}
	s.byTarget = map[string]string{}
	path := h.shortLinksPath()
	if path == "" {
		return errors.New("data dir is not configured")
	}
	stored := map[string]string{}
	if err := fileutil.ReadJSON(path, &stored); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for code, target := range stored {
		if !validShortCode(code) || !validShortTarget(target) {
			continue
		}
		s.byCode[code] = target
		// На случай ручной правки с дублями: адресу — один (первый попавшийся)
		// код, но рабочими остаются все.
		if _, dup := s.byTarget[target]; !dup {
			s.byTarget[target] = code
		}
	}
	s.loaded = true
	return nil
}

// shortLinks возвращает короткие ссылки («/s/<код>») для адресов, заводя коды
// тем, у кого их ещё нет (одна запись файла на вызов). Адрес, который не
// удалось обслужить, в ответе отсутствует — интерфейс тогда просто не
// показывает короткую ссылку.
func (h *Handlers) shortLinks(targets ...string) map[string]string {
	out := make(map[string]string, len(targets))
	if len(targets) == 0 {
		return out
	}
	s := &h.shortStore
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := h.loadShortLinksLocked(); err != nil {
		h.logger.Printf("WARN short links: %v", err)
		return out
	}
	created := make([]string, 0) // адреса, получившие код в этом вызове
	allowed := h.newShortTargetChecker()
	for _, target := range targets {
		if !validShortTarget(target) || !allowed.check(target) {
			continue
		}
		if code, ok := s.byTarget[target]; ok {
			out[target] = shortLinkPrefix + code
			continue
		}
		code, err := newShortCode(s.byCode)
		if err != nil {
			h.logger.Printf("WARN short links: %v", err)
			continue
		}
		s.byCode[code] = target
		s.byTarget[target] = code
		created = append(created, target)
		out[target] = shortLinkPrefix + code
	}
	if len(created) > 0 {
		if err := fileutil.WriteJSON(h.shortLinksPath(), s.byCode, 0o600); err != nil {
			// Не сохранили — новые коды не раздаём: после перезапуска они
			// повели бы в никуда.
			h.logger.Printf("WARN short links: save: %v", err)
			for _, target := range created {
				delete(s.byCode, s.byTarget[target])
				delete(s.byTarget, target)
				delete(out, target)
			}
		}
	}
	return out
}

// shortLink — короткая ссылка для одного адреса ("" — не вышло).
func (h *Handlers) shortLink(target string) string {
	return h.shortLinks(target)[target]
}

// newShortCode — случайный свободный код.
func newShortCode(taken map[string]string) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		code, err := randomShortCode()
		if err != nil {
			return "", err
		}
		if _, dup := taken[code]; !dup {
			return code, nil
		}
	}
	return "", errors.New("не удалось подобрать свободный короткий код")
}

// randomShortCode — shortCodeLen символов алфавита, равновероятно (байты,
// не попадающие в целое число алфавитов, отбрасываются — без смещения).
func randomShortCode() (string, error) {
	n := len(shortCodeAlphabet)
	limit := 256 - 256%n
	out := make([]byte, 0, shortCodeLen)
	buf := make([]byte, 16)
	for len(out) < shortCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, shortCodeAlphabet[int(b)%n])
			if len(out) == shortCodeLen {
				break
			}
		}
	}
	return string(out), nil
}

func validShortCode(code string) bool {
	if len(code) != shortCodeLen {
		return false
	}
	for i := 0; i < len(code); i++ {
		if !strings.ContainsRune(shortCodeAlphabet, rune(code[i])) {
			return false
		}
	}
	return true
}

// validShortTarget — только свои страницы с таблицами: путь от корня сайта,
// без схемы и хоста (никаких открытых редиректов наружу).
func validShortTarget(target string) bool {
	if !strings.HasPrefix(target, "/standings") || strings.HasPrefix(target, "//") {
		return false
	}
	u, err := url.Parse(target)
	return err == nil && u.Scheme == "" && u.Host == ""
}

// ── Какие ссылки получают короткую версию ───────────────────────────────────

// groupPageURL — адрес группы без токена (его раздают ученикам).
func groupPageURL(slug string) string { return "/standings/" + slug }

// accessEntryURL — ссылка доступа с токеном ("" — доступ по паролю или без
// токена). Глобальный доступ ведёт в каталог групп.
func accessEntryURL(entry domain.AccessEntry, slug string) string {
	if !entry.UsesToken() {
		return ""
	}
	base := "/standings"
	if slug != "" {
		base = groupPageURL(slug)
	}
	return base + "?token=" + url.QueryEscape(strings.TrimSpace(entry.Token))
}

// shortTargetChecker решает, можно ли адресу иметь короткую ссылку: да, если
// по нему нельзя ничего менять. Адрес без токена (группа, страница ученика) —
// можно. С токеном — только если КАЖДЫЙ доступ с этим токеном (групповой или
// глобальный, включённый или выключенный: включат — даст права) только на
// просмотр. Токен, которого нет ни у одного доступа, ничего не открывает —
// ссылка ведёт на публичный вид, её можно оставить. Доступы читаются один раз
// на проверяльщик.
type shortTargetChecker struct {
	h            *Handlers
	groups       map[string][]domain.AccessEntry
	global       []domain.AccessEntry
	globalLoaded bool
}

func (h *Handlers) newShortTargetChecker() *shortTargetChecker {
	return &shortTargetChecker{h: h, groups: map[string][]domain.AccessEntry{}}
}

func (c *shortTargetChecker) check(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	token := strings.TrimSpace(u.Query().Get("token"))
	if token == "" {
		return true
	}
	if !c.globalLoaded {
		c.global = c.h.loadGlobalAccesses()
		c.globalLoaded = true
	}
	entries := append([]domain.AccessEntry(nil), c.global...)
	if slug, ok := strings.CutPrefix(u.Path, "/standings/"); ok && domain.IsValidSlug(slug) {
		group, cached := c.groups[slug]
		if !cached {
			group = c.h.groupAccesses(slug)
			c.groups[slug] = group
		}
		entries = append(entries, group...)
	}
	for _, e := range entries {
		if e.Auth == domain.AccessAuthToken && strings.TrimSpace(e.Token) == token && !domain.ReadOnlyPerms(e.Perms) {
			return false
		}
	}
	return true
}

// EnsureShortLinks заводит короткие ссылки всем существующим ссылкам на
// таблицы, по которым нельзя ничего менять (группам, доступам только на
// просмотр, страницам учеников для родителей), и вычищает коды ссылок, которым
// короткая версия больше не положена (доступу дали права на изменение).
// Вызывается при старте сервера; новые ссылки дальше получают код при первом
// показе. Возвращает, сколько кодов заведено и сколько удалено.
func (h *Handlers) EnsureShortLinks() (created, removed int) {
	targets := make([]string, 0)
	for _, slug := range h.allGroupSlugs() {
		targets = append(targets, groupPageURL(slug))
		for _, entry := range h.groupAccesses(slug) {
			if u := accessEntryURL(entry, slug); u != "" {
				targets = append(targets, u)
			}
		}
	}
	for _, entry := range h.loadGlobalAccesses() {
		if u := accessEntryURL(entry, ""); u != "" {
			targets = append(targets, u)
		}
	}
	if h.StudentLinksStatus() == nil {
		for id := range h.loadStudentsByIDFromDataDir() {
			if u := h.studentLinkURL(id); u != "" {
				targets = append(targets, u)
			}
		}
	}

	removed = h.purgeShortLinks()

	h.shortStore.mu.Lock()
	before := len(h.shortStore.byCode)
	h.shortStore.mu.Unlock()
	h.shortLinks(targets...)
	h.shortStore.mu.Lock()
	defer h.shortStore.mu.Unlock()
	return len(h.shortStore.byCode) - before, removed
}

// purgeShortLinks удаляет из файла коды ссылок, которым короткая версия не
// положена. Возвращает, сколько удалено.
func (h *Handlers) purgeShortLinks() int {
	s := &h.shortStore
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := h.loadShortLinksLocked(); err != nil {
		h.logger.Printf("WARN short links: %v", err)
		return 0
	}
	allowed := h.newShortTargetChecker()
	removed := make(map[string]string)
	for code, target := range s.byCode {
		if !allowed.check(target) {
			removed[code] = target
		}
	}
	if len(removed) == 0 {
		return 0
	}
	for code, target := range removed {
		delete(s.byCode, code)
		if s.byTarget[target] == code {
			delete(s.byTarget, target)
		}
	}
	if err := fileutil.WriteJSON(h.shortLinksPath(), s.byCode, 0o600); err != nil {
		// Файл не переписали — не беда: переход по таким кодам всё равно
		// закрыт проверкой в ShortLinkRedirect.
		h.logger.Printf("WARN short links: save after purge: %v", err)
	}
	return len(removed)
}

// ── Переход по короткой ссылке ──────────────────────────────────────────────

// ShortLinkRedirect — /s/<код> → полный адрес. Неизвестный код — 404; с адреса,
// который часто промахивается, — 429 до конца окна (защита от перебора).
func (h *Handlers) ShortLinkRedirect(w http.ResponseWriter, r *http.Request) {
	// За короткой ссылкой может стоять токен или ключ страницы ученика: не
	// отдаём их дальше в Referer и не кэшируем редирект.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")

	client := shortLimitKey(r)
	if h.shortMissesExceeded(client) {
		http.Error(w, "слишком много неверных коротких ссылок, попробуйте позже", http.StatusTooManyRequests)
		return
	}
	code := r.PathValue("code")
	target := ""
	if validShortCode(code) {
		s := &h.shortStore
		s.mu.Lock()
		if err := h.loadShortLinksLocked(); err == nil {
			target = s.byCode[code]
		}
		s.mu.Unlock()
	}
	// Код настоящий, но ссылке короткая версия больше не положена (доступу
	// дали права на изменение) — как будто кода нет. Это не перебор, в лимит
	// промахов не засчитываем.
	if target != "" && !h.newShortTargetChecker().check(target) {
		http.NotFound(w, r)
		return
	}
	if target == "" {
		h.recordShortMiss(client)
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// shortLimitKey — адрес клиента для ограничения перебора. Берём ПОСЛЕДНИЙ
// адрес X-Forwarded-For (его дописал наш обратный прокси), а не первый, как в
// журнале: первый клиент пишет сам и подменил бы его на каждый запрос.
func shortLimitKey(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		parts := strings.Split(fwd, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *Handlers) shortMissesExceeded(client string) bool {
	s := &h.shortStore
	s.missMu.Lock()
	defer s.missMu.Unlock()
	c, ok := s.misses[client]
	if !ok || time.Since(c.since) > shortMissWindow {
		return false
	}
	return c.count >= shortMissLimit
}

func (h *Handlers) recordShortMiss(client string) {
	s := &h.shortStore
	s.missMu.Lock()
	defer s.missMu.Unlock()
	if s.misses == nil {
		s.misses = make(map[string]shortMissCounter)
	}
	now := time.Now()
	// Заодно подчищаем истёкшие окна: записей не больше, чем промахивающихся
	// адресов за окно.
	for k, c := range s.misses {
		if now.Sub(c.since) > shortMissWindow {
			delete(s.misses, k)
		}
	}
	c := s.misses[client]
	if c.count == 0 {
		c.since = now
	}
	c.count++
	s.misses[client] = c
}
