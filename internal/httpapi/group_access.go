package httpapi

// Доступ к страницам группы. Четыре уровня, каждый включает предыдущий:
//
//	RoleGuest    — «Гость», без токена: публичная страница (заморозка, скрытое
//	               вырезано);
//	RoleObserver — «Наблюдатель», ?token=<group_secret_token>: разморозка,
//	               скрытые контесты, участники, профили, экспорт в Excel.
//	               Ничего не меняет;
//	RoleJury     — «Жюри», логин/пароль: + ручные оценки, настройка таблицы
//	               оценок (столбцы, веса), кондуиты, разметка флагов;
//	RoleAdmin    — «Админ», логин/пароль: + управление контестами группы
//	               (добавить/убрать/переставить/настроить, inline-контесты).
//
// Вход в панель — /standings/<slug>/panel: браузерный Basic Auth (как в главной
// админке), роль определяется совпавшей парой учёток. После входа сервер
// встраивает в страницу role-token — HMAC(секрет, slug|роль|логин|пароль),
// который JS кладёт в тело API-запросов (как раньше клался токен группы).
// Секрет не хранит состояния: проверка — пересчёт формулы. Смена пароля
// автоматически обесценивает выданные раньше role-token'ы.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

// GroupRole — уровень доступа к группе (по возрастанию прав).
type GroupRole int

const (
	RoleGuest GroupRole = iota
	RoleObserver
	RoleJury
	RoleAdmin
)

// AtLeast — роль не ниже требуемой.
func (r GroupRole) AtLeast(min GroupRole) bool { return r >= min }

// String — машинное имя роли (идёт в role-token и в шаблоны).
func (r GroupRole) String() string {
	switch r {
	case RoleAdmin:
		return "admin"
	case RoleJury:
		return "jury"
	case RoleObserver:
		return "observer"
	}
	return "guest"
}

// Title — человекочитаемое имя роли для страницы.
func (r GroupRole) Title() string {
	switch r {
	case RoleAdmin:
		return "Админ"
	case RoleJury:
		return "Жюри"
	case RoleObserver:
		return "Наблюдатель"
	}
	return "Гость"
}

// panelSecret возвращает секрет подписи role-token'ов. Хранится в
// data/credentials/panel_secret.json; при первом обращении создаётся (32
// случайных байта). Потеря файла = разлогин всех панелей, не более.
func (h *Handlers) panelSecret() []byte {
	h.panelSecretMu.Lock()
	defer h.panelSecretMu.Unlock()
	if len(h.panelSecretValue) > 0 {
		return h.panelSecretValue
	}
	h.panelSecretValue = h.loadOrCreatePanelSecret()
	return h.panelSecretValue
}

// loadOrCreatePanelSecret читает секрет с диска, а если его нет — создаёт.
// Вызывается под panelSecretMu.
func (h *Handlers) loadOrCreatePanelSecret() []byte {
	if h.dataDir == "" {
		return nil
	}
	path := filepath.Join(h.dataDir, "credentials", "panel_secret.json")

	var cfg struct {
		Secret string `json:"secret"`
	}
	if body, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(body, &cfg) == nil {
			if raw, err := hex.DecodeString(strings.TrimSpace(cfg.Secret)); err == nil && len(raw) >= 16 {
				return raw
			}
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		h.logger.Printf("ERROR generate panel secret: %v", err)
		return nil
	}
	cfg.Secret = hex.EncodeToString(raw)
	if err := fileutil.WriteJSON(path, cfg, 0o600); err != nil {
		// Не смогли сохранить — секрет живёт до перезапуска (панель работает,
		// но после рестарта потребуется повторный вход).
		h.logger.Printf("WARN save panel secret to %s: %v", path, err)
	}
	return raw
}

// roleTokenFor — подпись роли для группы. Пустая строка — учётки нет.
func (h *Handlers) roleTokenFor(slug string, role GroupRole, cred *domain.GroupPanelCredential) string {
	secret := h.panelSecret()
	if len(secret) == 0 || !cred.Valid() {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(slug + "\x00" + role.String() + "\x00" + strings.TrimSpace(cred.Login) + "\x00" + cred.Password))
	return hex.EncodeToString(mac.Sum(nil))
}

// panelRoleByCredentials — роль по логину/паролю («Админ» старше «Жюри»). Сравнение
// constant-time, чтобы не подсказывать таймингом.
func panelRoleByCredentials(gf domain.GroupFile, login, password string) GroupRole {
	if gf.PanelAccess == nil {
		return RoleGuest
	}
	match := func(c *domain.GroupPanelCredential) bool {
		if !c.Valid() {
			return false
		}
		loginOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(login)), []byte(strings.TrimSpace(c.Login))) == 1
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(c.Password)) == 1
		return loginOK && passOK
	}
	if match(gf.PanelAccess.Admin) {
		return RoleAdmin
	}
	if match(gf.PanelAccess.Jury) {
		return RoleJury
	}
	return RoleGuest
}

// panelRoleByToken — роль по role-token'у из тела запроса.
func (h *Handlers) panelRoleByToken(slug, roleToken string) GroupRole {
	roleToken = strings.TrimSpace(roleToken)
	if roleToken == "" {
		return RoleGuest
	}
	gf, ok := h.readSourceGroupFile(slug)
	if !ok || gf.PanelAccess == nil {
		return RoleGuest
	}
	for _, cand := range []struct {
		role GroupRole
		cred *domain.GroupPanelCredential
	}{
		{RoleAdmin, gf.PanelAccess.Admin},
		{RoleJury, gf.PanelAccess.Jury},
	} {
		want := h.roleTokenFor(slug, cand.role, cand.cred)
		if want != "" && subtle.ConstantTimeCompare([]byte(roleToken), []byte(want)) == 1 {
			return cand.role
		}
	}
	return RoleGuest
}

// groupRole — итоговая роль запроса: максимум из role-token (тело/параметр) и
// токена группы (?token=). Для страниц roleToken пустой — роль по токену.
func (h *Handlers) groupRole(slug, token, roleToken string) GroupRole {
	if !domain.IsValidSlug(slug) {
		return RoleGuest
	}
	if role := h.panelRoleByToken(slug, roleToken); role > RoleGuest {
		return role
	}
	if strings.TrimSpace(token) != "" && h.groupTokenValid(slug, strings.TrimSpace(token)) {
		return RoleObserver
	}
	return RoleGuest
}

// requirePanelRole — общая проверка для API панели: разбирает slug/role_token из
// уже раскодированного запроса и отвечает 403, если прав не хватает.
func (h *Handlers) requirePanelRole(w http.ResponseWriter, slug, roleToken string, min GroupRole) bool {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return false
	}
	if !h.groupRole(slug, "", roleToken).AtLeast(min) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "недостаточно прав: войдите в панель группы"})
		return false
	}
	return true
}

// panelChallenge — предложить браузеру ввести логин/пароль панели группы.
// Realm свой у каждой группы: браузер держит учётки групп раздельно.
func panelChallenge(w http.ResponseWriter, slug string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="group-`+slug+`"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// authorizePanelRequest — вход в панель по Basic Auth. Возвращает роль и
// role-token для встраивания в страницу; false — ответ уже отправлен
// (челлендж или 404, если панель у группы не настроена).
func (h *Handlers) authorizePanelRequest(w http.ResponseWriter, r *http.Request, slug string) (GroupRole, string, bool) {
	if !domain.IsValidSlug(slug) {
		http.NotFound(w, r)
		return RoleGuest, "", false
	}
	gf, ok := h.readSourceGroupFile(slug)
	if !ok || !gf.PanelConfigured() {
		// Панель не настроена — не раскрываем факт существования группы больше,
		// чем публичная страница: обычная 404.
		http.NotFound(w, r)
		return RoleGuest, "", false
	}
	user, pass, hasAuth := r.BasicAuth()
	if !hasAuth {
		panelChallenge(w, slug)
		return RoleGuest, "", false
	}
	role := panelRoleByCredentials(gf, user, pass)
	if role == RoleGuest {
		panelChallenge(w, slug)
		return RoleGuest, "", false
	}
	cred := gf.PanelAccess.Jury
	if role == RoleAdmin {
		cred = gf.PanelAccess.Admin
	}
	return role, h.roleTokenFor(slug, role, cred), true
}

// groupTokenOf — секретный токен группы (для ссылок внутри панели: участники,
// сводная, экспорт работают по токену). Пусто — токена нет.
func (h *Handlers) groupTokenOf(slug string) string {
	gf, ok := h.readSourceGroupFile(slug)
	if !ok {
		return ""
	}
	return strings.TrimSpace(gf.GroupSecretToken)
}
