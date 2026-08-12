package httpapi

// Резолв доступа к группе: кто пришёл и что ему можно.
//
// Подтвердить доступ можно тремя способами:
//   - ?token=… в адресе (доступы со входом по ссылке);
//   - кука сессии, выданная после входа по логину и паролю;
//   - заголовок Basic Auth (сама дверь входа /panel).
//
// Права всех подтверждённых и покрывающих группу доступов ОБЪЕДИНЯЮТСЯ: пришёл
// с глобальной сессией куратора и локальной ссылкой жюри — получит и то и то.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"standings-edu/internal/domain"
)

const (
	// accessCookieName — сессия, выданная после входа по логину и паролю.
	accessCookieName = "sedu_access"
	// accessSessionTTL — срок сессии; после него нужен повторный вход.
	accessSessionTTL = 12 * time.Hour
)

// GroupAccess — итог резолва: что подтверждено и какие права получились.
type GroupAccess struct {
	// Perms — объединение прав всех подтверждённых доступов.
	Perms domain.PermSet
	// Entries — сами записи (для подписи в шапке и журнала).
	Entries []domain.AccessEntry
	// Token — токен группы, которым пришли (для ссылок внутри страниц). Пусто —
	// вошли по сессии или паролю.
	Token string
	// SignedIn — доступ подтверждён сессией/паролем (есть кнопка «Выйти»).
	SignedIn bool
}

// Has — есть право.
func (a *GroupAccess) Has(p domain.Perm) bool {
	if a == nil {
		return false
	}
	return a.Perms.Has(p)
}

// HasAny — есть хотя бы одно из прав.
func (a *GroupAccess) HasAny(perms ...domain.Perm) bool {
	if a == nil {
		return false
	}
	return a.Perms.HasAny(perms...)
}

// Elevated — доступ вообще что-то даёт сверх публичного вида.
func (a *GroupAccess) Elevated() bool { return a != nil && !a.Perms.Empty() }

// Title — подпись доступа для шапки страницы («Жюри — Иванов» или несколько).
func (a *GroupAccess) Title() string {
	if a == nil || len(a.Entries) == 0 {
		return ""
	}
	titles := make([]string, 0, len(a.Entries))
	for _, e := range a.Entries {
		titles = append(titles, e.Title)
	}
	return strings.Join(titles, " + ")
}

// LinkQuery — хвост «?token=…» для ссылок внутри страниц. Пусто, если вошли по
// сессии: тогда доступ подтверждается кукой и токен в адресе не нужен.
func (a *GroupAccess) LinkQuery() string {
	if a == nil || a.Token == "" {
		return ""
	}
	return "?token=" + url.QueryEscape(a.Token)
}

// IsSignedIn — вошли по логину и паролю (в шаблоне: показывать «Выйти»).
func (a *GroupAccess) IsSignedIn() bool { return a != nil && a.SignedIn }

// Удобные обёртки для шаблонов: спрашивать `{{if .Access.CanEditGrades}}`
// читается лучше, чем сравнение строковых прав в HTML.

// CanViewParticipants — статистика участников и профили.
func (a *GroupAccess) CanViewParticipants() bool { return a.Has(domain.PermViewParticipants) }

// CanExport — выгрузка группы в Excel.
func (a *GroupAccess) CanExport() bool { return a.Has(domain.PermViewExport) }

// CanEditGrades — открыть редактор оценок (сами оценки или их настройка).
func (a *GroupAccess) CanEditGrades() bool {
	return a.HasAny(domain.PermGradesManual, domain.PermGradesConfig)
}

// CanFillKonduit — заполнять кондуиты.
func (a *GroupAccess) CanFillKonduit() bool { return a.Has(domain.PermKonduitFill) }

// CanCreateKonduit — создавать кондуиты.
func (a *GroupAccess) CanCreateKonduit() bool { return a.Has(domain.PermKonduitCreate) }

// CanManageContests — управление списком контестов группы.
func (a *GroupAccess) CanManageContests() bool { return a.Has(domain.PermContestsManage) }

// CanReviewFlags — разметка флагов нечестности.
func (a *GroupAccess) CanReviewFlags() bool { return a.Has(domain.PermFlagsReview) }

// HasPanel — есть хоть одно действие: показывать ли панель на странице группы.
func (a *GroupAccess) HasPanel() bool {
	return a.HasAny(domain.PermGradesManual, domain.PermGradesConfig, domain.PermKonduitFill,
		domain.PermKonduitCreate, domain.PermContestsManage, domain.PermContestsInline)
}

// ViewKey — отпечаток прав, влияющих на ВИД таблиц: ключ кэша готовых ответов
// (у разных доступов разные разморозка, скрытое, ссылки на задачи и судью).
func (a *GroupAccess) ViewKey() string {
	key := make([]byte, 0, 4)
	for _, p := range []domain.Perm{domain.PermViewUnfrozen, domain.PermViewHidden,
		domain.PermViewTaskLinks, domain.PermViewJudgeLinks} {
		if a.Has(p) {
			key = append(key, '1')
		} else {
			key = append(key, '0')
		}
	}
	return string(key)
}

// accessSecret — ключ подписи сессий и токенов действий (общий с панелью).
func (h *Handlers) accessSecret() []byte { return h.panelSecret() }

// entryFingerprint — отпечаток записи доступа: id + секретная часть. Меняется
// при смене пароля/токена, поэтому старые сессии сами перестают работать.
func entryFingerprint(entry domain.AccessEntry) string {
	secretPart := entry.Token
	if entry.UsesPassword() {
		secretPart = entry.Login + "\x00" + entry.Password
	}
	sum := sha256.Sum256([]byte(entry.ID + "\x00" + secretPart))
	return hex.EncodeToString(sum[:8])
}

// sessionValue — содержимое куки: список «id:отпечаток» с меткой истечения и
// подписью. Хранить состояние на сервере не нужно.
func (h *Handlers) sessionValue(refs []string, exp int64) string {
	payload := strconv.FormatInt(exp, 10) + "|" + strings.Join(refs, ",")
	mac := hmac.New(sha256.New, h.accessSecret())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + hex.EncodeToString(mac.Sum(nil))))
}

// parseSessionValue проверяет подпись и срок; возвращает ссылки «id:отпечаток».
func (h *Handlers) parseSessionValue(value string) []string {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return nil
	}
	payload, sig := parts[0]+"|"+parts[1], parts[2]
	mac := hmac.New(sha256.New, h.accessSecret())
	mac.Write([]byte(payload))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) != 1 {
		return nil
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return nil
	}
	if strings.TrimSpace(parts[1]) == "" {
		return nil
	}
	return strings.Split(parts[1], ",")
}

// setAccessSession выдаёт (или дополняет) сессию: к уже подтверждённым доступам
// добавляются новые. Кука — HttpOnly, на весь сайт: доступ может быть
// глобальным, да и группа у пользователя обычно не одна.
func (h *Handlers) setAccessSession(w http.ResponseWriter, r *http.Request, entries ...domain.AccessEntry) {
	refs := make([]string, 0, len(entries)+2)
	seen := map[string]struct{}{}
	if c, err := r.Cookie(accessCookieName); err == nil {
		for _, ref := range h.parseSessionValue(c.Value) {
			if _, dup := seen[ref]; !dup && strings.TrimSpace(ref) != "" {
				seen[ref] = struct{}{}
				refs = append(refs, ref)
			}
		}
	}
	for _, e := range entries {
		ref := e.ID + ":" + entryFingerprint(e)
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return
	}
	exp := time.Now().Add(accessSessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     accessCookieName,
		Value:    h.sessionValue(refs, exp.Unix()),
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
	})
}

// clearAccessSession — выход: кука гасится.
func clearAccessSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: accessCookieName, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// candidateAccesses — все доступы, применимые к группе: её собственные и
// глобальные, покрывающие её.
func (h *Handlers) candidateAccesses(slug string) []domain.AccessEntry {
	out := make([]domain.AccessEntry, 0, 8)
	for _, e := range h.groupAccesses(slug) {
		if e.IsEnabled() {
			out = append(out, e)
		}
	}
	for _, e := range h.loadGlobalAccesses() {
		if e.IsEnabled() && e.CoversGroup(slug) {
			out = append(out, e)
		}
	}
	return out
}

// resolveAccess определяет права запроса на группу.
func (h *Handlers) resolveAccess(slug string, r *http.Request) *GroupAccess {
	acc := &GroupAccess{Perms: domain.PermSet{}}
	if !domain.IsValidSlug(slug) {
		return acc
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	sessionRefs := map[string]struct{}{}
	if c, err := r.Cookie(accessCookieName); err == nil {
		for _, ref := range h.parseSessionValue(c.Value) {
			sessionRefs[ref] = struct{}{}
		}
	}
	basicUser, basicPass, hasBasic := r.BasicAuth()

	for _, entry := range h.candidateAccesses(slug) {
		matched := false
		switch {
		case token != "" && entry.UsesToken() &&
			subtle.ConstantTimeCompare([]byte(token), []byte(strings.TrimSpace(entry.Token))) == 1:
			matched = true
			acc.Token = token
		case hasBasic && entry.UsesPassword() && h.credentialsMatch(entry, basicUser, basicPass):
			matched = true
			acc.SignedIn = true
		}
		if !matched {
			if _, ok := sessionRefs[entry.ID+":"+entryFingerprint(entry)]; ok {
				matched = true
				acc.SignedIn = true
			}
		}
		if matched {
			acc.Perms.Add(entry.Perms)
			acc.Entries = append(acc.Entries, entry)
		}
	}
	// Токен для ссылок внутри страниц: если вошли по паролю, подставим любой
	// токен-доступ этой группы, чтобы ссылки «поделиться» продолжали работать.
	if acc.Token == "" && acc.Elevated() {
		acc.Token = h.anyGroupToken(slug)
	}
	return acc
}

// credentialsMatch — логин и пароль записи. Логин сверяется constant-time,
// пароль — с хешем (см. domain.VerifyPassword), с кэшем удачных проверок.
func (h *Handlers) credentialsMatch(entry domain.AccessEntry, login, password string) bool {
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(login)), []byte(strings.TrimSpace(entry.Login))) != 1 {
		return false
	}
	if password == "" {
		return false
	}
	key := entry.ID + "\x00" + digest(entry.Password) + "\x00" + digest(password)
	if h.passwordCached(key) {
		return true
	}
	if !domain.VerifyPassword(entry.Password, password) {
		return false
	}
	h.rememberPassword(key)
	return true
}

// digest — отпечаток строки для ключа кэша (сам пароль в памяти не держим).
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// passwordCacheTTL — насколько удачная проверка избавляет от повторного счёта.
const passwordCacheTTL = 5 * time.Minute

func (h *Handlers) passwordCached(key string) bool {
	h.pwCacheMu.Lock()
	defer h.pwCacheMu.Unlock()
	until, ok := h.pwCache[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(h.pwCache, key)
		return false
	}
	return true
}

func (h *Handlers) rememberPassword(key string) {
	h.pwCacheMu.Lock()
	defer h.pwCacheMu.Unlock()
	if h.pwCache == nil {
		h.pwCache = make(map[string]time.Time)
	}
	now := time.Now()
	// Заодно подчищаем просроченное: записей мало (по одной на вошедшего).
	for k, until := range h.pwCache {
		if now.After(until) {
			delete(h.pwCache, k)
		}
	}
	h.pwCache[key] = now.Add(passwordCacheTTL)
}

// anyGroupToken — токен первого включённого токен-доступа группы ("" — нет).
func (h *Handlers) anyGroupToken(slug string) string {
	for _, e := range h.groupAccesses(slug) {
		if e.IsEnabled() && e.UsesToken() {
			return strings.TrimSpace(e.Token)
		}
	}
	return ""
}

// resolveGlobalAccess — доступ вне группы (каталог): токен, сессия или Basic.
func (h *Handlers) resolveGlobalAccess(r *http.Request) *GroupAccess {
	acc := &GroupAccess{Perms: domain.PermSet{}}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	sessionRefs := map[string]struct{}{}
	if c, err := r.Cookie(accessCookieName); err == nil {
		for _, ref := range h.parseSessionValue(c.Value) {
			sessionRefs[ref] = struct{}{}
		}
	}
	basicUser, basicPass, hasBasic := r.BasicAuth()

	for _, entry := range h.loadGlobalAccesses() {
		if !entry.IsEnabled() {
			continue
		}
		matched := false
		switch {
		case token != "" && entry.UsesToken() &&
			subtle.ConstantTimeCompare([]byte(token), []byte(strings.TrimSpace(entry.Token))) == 1:
			matched = true
			acc.Token = token
		case hasBasic && entry.UsesPassword() && h.credentialsMatch(entry, basicUser, basicPass):
			matched = true
			acc.SignedIn = true
		}
		if !matched {
			if _, ok := sessionRefs[entry.ID+":"+entryFingerprint(entry)]; ok {
				matched = true
				acc.SignedIn = true
			}
		}
		if matched {
			acc.Perms.Add(entry.Perms)
			acc.Entries = append(acc.Entries, entry)
		}
	}
	return acc
}

// requirePerm — проверка права для API. Пустое сообщение об ошибке уже отдано.
func (h *Handlers) requirePerm(w http.ResponseWriter, r *http.Request, slug string, perm domain.Perm) (*GroupAccess, bool) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return nil, false
	}
	acc := h.resolveAccess(slug, r)
	if !acc.Has(perm) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "недостаточно прав для этого действия"})
		return nil, false
	}
	return acc, true
}

// accessChallenge — предложить браузеру ввести логин и пароль доступа.
func accessChallenge(w http.ResponseWriter, realm string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// signInToGroup — дверь входа в группу по логину и паролю. Уже подтверждённый
// доступ (токен в адресе или живая сессия) пропускается без спроса. Иначе —
// Basic-челлендж; после успеха выдаётся сессия. false — ответ уже отправлен.
func (h *Handlers) signInToGroup(w http.ResponseWriter, r *http.Request, slug string) (*GroupAccess, bool) {
	if !domain.IsValidSlug(slug) {
		http.NotFound(w, r)
		return nil, false
	}
	acc := h.resolveAccess(slug, r)
	if acc.Elevated() {
		// Вошли по паролю прямо сейчас — запоминаем сессией, чтобы дальше
		// работали обычные адреса без повторного ввода.
		if _, _, hasBasic := r.BasicAuth(); hasBasic {
			h.setAccessSession(w, r, acc.Entries...)
			h.audit(r, auditEntry{Action: "access.signin", Group: slug, Actor: acc.Title(), OK: true})
		}
		return acc, true
	}
	// Нечего спрашивать — у группы нет доступов со входом по паролю.
	if !h.groupHasPasswordAccess(slug) {
		http.NotFound(w, r)
		return nil, false
	}
	if login, _, hasBasic := r.BasicAuth(); hasBasic {
		h.audit(r, auditEntry{Action: "access.signin", Group: slug, Actor: login, OK: false, Detail: "неверный логин или пароль"})
	}
	accessChallenge(w, "group-"+slug)
	return nil, false
}

// groupHasPasswordAccess — есть ли у группы (или у покрывающего её глобального
// доступа) вход по логину и паролю.
func (h *Handlers) groupHasPasswordAccess(slug string) bool {
	for _, e := range h.candidateAccesses(slug) {
		if e.UsesPassword() {
			return true
		}
	}
	return false
}

// GlobalSignIn — дверь входа по логину и паролю для ГЛОБАЛЬНЫХ доступов:
// спрашивает учётку и уводит в каталог групп. У доступов группы такая дверь
// своя (/standings/<slug>/panel), а глобальному входить некуда — с этого
// адреса и начинают преподаватели, у которых доступ сразу ко всем группам.
func (h *Handlers) GlobalSignIn(w http.ResponseWriter, r *http.Request) {
	acc := h.resolveGlobalAccess(r)
	if acc.Elevated() {
		// Вошли по паролю прямо сейчас — запоминаем сессией, дальше работают
		// обычные адреса без повторного ввода.
		if _, _, hasBasic := r.BasicAuth(); hasBasic {
			h.setAccessSession(w, r, acc.Entries...)
			h.audit(r, auditEntry{Action: "access.signin", Actor: acc.Title(), OK: true, Detail: "глобальный доступ"})
		}
		http.Redirect(w, r, "/standings", http.StatusSeeOther)
		return
	}
	// Нечего спрашивать — глобальных доступов со входом по паролю нет.
	if !h.hasGlobalPasswordAccess() {
		http.NotFound(w, r)
		return
	}
	if login, _, hasBasic := r.BasicAuth(); hasBasic {
		h.audit(r, auditEntry{Action: "access.signin", Actor: login, OK: false, Detail: "глобальный доступ: неверный логин или пароль"})
	}
	accessChallenge(w, "standings")
}

// hasGlobalPasswordAccess — есть ли глобальный доступ со входом по паролю.
func (h *Handlers) hasGlobalPasswordAccess() bool {
	for _, e := range h.loadGlobalAccesses() {
		if e.IsEnabled() && e.UsesPassword() {
			return true
		}
	}
	return false
}

// AccessSignOut — выход: гасим сессию и возвращаем на страницу группы.
func (h *Handlers) AccessSignOut(w http.ResponseWriter, r *http.Request) {
	clearAccessSession(w)
	back := strings.TrimSpace(r.URL.Query().Get("back"))
	if !strings.HasPrefix(back, "/standings") {
		back = "/standings"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
