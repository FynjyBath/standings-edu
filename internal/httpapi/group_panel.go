package httpapi

// Панель группы: страницы и API для ролей «Жюри» и «Админ».
// Вход — /standings/<slug>/panel (Basic Auth по учёткам из group.json), дальше
// каждая операция подтверждается role-token'ом (см. group_access.go).
//
// Логика операций не дублируется: панель зовёт те же внутренние helpers, что и
// админка (addGroupContestRef, moveGroupContestEntry, saveInlineContest…), а
// разметка управления контестами — общий partial groupContests.

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"standings-edu/internal/domain"
)

// GroupPanelContestsPageData — страница управления контестами в панели.
type GroupPanelContestsPageData struct {
	AdminGroupManagePageData
	// RoleToken/RoleTitle — подпись роли для API и подпись в шапке.
	RoleToken string
	RoleTitle string
	// GroupToken — токен группы для ссылок «страница группы» из панели.
	GroupToken string
}

// GroupPanelContestsPage — управление контестами группы (роль «Админ»):
// та же таблица и та же форма контеста, что в админке.
func (h *Handlers) GroupPanelContestsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	role, roleToken, ok := h.authorizePanelRequest(w, r, slug)
	if !ok {
		return
	}
	if !role.AtLeast(RoleAdmin) {
		http.Error(w, "нужны права роли «Админ»", http.StatusForbidden)
		return
	}
	base, found, err := h.buildGroupManageData(slug)
	if err != nil {
		h.logger.Printf("ERROR panel contests slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	base.PageTitle = "Контесты: " + base.GroupTitle
	w.Header().Set("Cache-Control", "no-store")
	page := GroupPanelContestsPageData{
		AdminGroupManagePageData: base,
		RoleToken:                roleToken,
		RoleTitle:                role.Title(),
		GroupToken:               h.groupTokenOf(slug),
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_panel_contests.html", page); err != nil {
		h.logger.Printf("ERROR render panel contests slug=%s: %v", slug, err)
	}
}

// GroupPanelGradesPage — редактор оценок группы (роль «Жюри»): настройка
// столбцов (веса, метрика, нормировка, коэффициенты) и сетка ручных оценок.
// Тот же шаблон, что в админке.
func (h *Handlers) GroupPanelGradesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	role, roleToken, ok := h.authorizePanelRequest(w, r, slug)
	if !ok {
		return
	}
	if !role.AtLeast(RoleJury) {
		http.Error(w, "нужны права роли «Жюри»", http.StatusForbidden)
		return
	}
	page, found, err := h.buildGroupGradesData(slug)
	if err != nil {
		h.logger.Printf("ERROR panel grades slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	page.RoleToken = roleToken
	page.RoleTitle = role.Title()
	page.GroupToken = h.groupTokenOf(slug)
	page.APIBase = "/api/group-panel"
	page.BackURL = "/standings/" + slug + "/panel"
	page.BackLabel = "← В панель группы"
	w.Header().Set("Cache-Control", "no-store")
	if err := h.renderer.Render(w, http.StatusOK, "admin_group_grades.html", page); err != nil {
		h.logger.Printf("ERROR render panel grades slug=%s: %v", slug, err)
	}
}

// ── API панели ───────────────────────────────────────────────────────────────
// Пути зеркалят админские (/api/admin/group/... → /api/group-panel/...), тела
// запросов те же плюс role_token.

// panelContestRequest — общая часть тел запросов панели по контестам.
type panelContestRequest struct {
	Slug      string `json:"slug"`
	RoleToken string `json:"role_token"`
}

// PanelContestAddRef — добавить в группу ссылку на глобальный контест.
func (h *Handlers) PanelContestAddRef(w http.ResponseWriter, r *http.Request) {
	var req struct {
		panelContestRequest
		ID string `json:"id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.requirePanelRole(w, slug, req.RoleToken, RoleAdmin) {
		return
	}
	if status, msg := h.addGroupContestRef(slug, strings.TrimSpace(req.ID)); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PanelContestMove — переставить контест группы.
func (h *Handlers) PanelContestMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		panelContestRequest
		ID  string `json:"id"`
		Dir string `json:"dir"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.requirePanelRole(w, slug, req.RoleToken, RoleAdmin) {
		return
	}
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if status, msg := h.moveGroupContestEntry(slug, strings.TrimSpace(req.ID), strings.TrimSpace(req.Dir), entries); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PanelContestRemove — убрать контест из группы.
func (h *Handlers) PanelContestRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		panelContestRequest
		ID string `json:"id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.requirePanelRole(w, slug, req.RoleToken, RoleAdmin) {
		return
	}
	if status, msg := h.removeGroupContestEntry(slug, strings.TrimSpace(req.ID)); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PanelContestSetOptions — настройки записи контеста в группе.
func (h *Handlers) PanelContestSetOptions(w http.ResponseWriter, r *http.Request) {
	var req adminGroupContestOptionsRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.requirePanelRole(w, slug, req.RoleToken, RoleAdmin) {
		return
	}
	if status, msg := h.setGroupContestOptions(slug, req); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PanelContestInlineSave — создать/изменить inline-контест группы. В панели, в
// отличие от админки, окно контеста ОБЯЗАТЕЛЬНО: набор задач без времени начала
// и конца завести нельзя (provider-контесты окно не используют — там не нужно).
func (h *Handlers) PanelContestInlineSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		adminContestSaveRequest
		Slug      string `json:"slug"`
		RoleToken string `json:"role_token"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.requirePanelRole(w, slug, req.RoleToken, RoleAdmin) {
		return
	}
	// Окно обязательно у контестов-наборов задач; provider-контесты его не
	// используют (результаты берутся из источника целиком).
	if !strings.EqualFold(strings.TrimSpace(req.SourceType), "provider") {
		if strings.TrimSpace(req.StartTime) == "" || strings.TrimSpace(req.EndTime) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "укажите время начала и конца контеста"})
			return
		}
	}
	id, status, msg := h.saveInlineContest(slug, req.adminContestSaveRequest, nil)
	if msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// PanelGradesSave — ручные оценки группы (роль «Жюри»).
func (h *Handlers) PanelGradesSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug      string                        `json:"slug"`
		RoleToken string                        `json:"role_token"`
		Grades    map[string]map[string]float64 `json:"grades"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.requirePanelRole(w, slug, req.RoleToken, RoleJury) {
		return
	}
	if status, msg := h.saveManualGrades(slug, req.Grades); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PanelGradesConfigSave — настройка таблицы оценок: столбцы, веса, метрика,
// нормировка, коэффициент дорешки, учёт пропущенных контестов (роль «Жюри»).
func (h *Handlers) PanelGradesConfigSave(w http.ResponseWriter, r *http.Request) {
	var req adminGradesConfigSaveRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.requirePanelRole(w, slug, req.RoleToken, RoleJury) {
		return
	}
	if status, msg := h.saveGradesConfig(slug, req); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// panelGroupFile — group.json группы для операций панели (короткий helper).
func (h *Handlers) panelGroupFile(slug string) (domain.GroupFile, bool) {
	gf, ok, err := h.readGroupFile(slug)
	if err != nil {
		return domain.GroupFile{}, false
	}
	return gf, ok
}

// AdminGroupPanelAccessSet — задать/убрать учётку панели группы (из админки).
// role: "jury" | "admin"; clear=true — убрать. При задании учётки у группы
// автоматически появляется токен (без него ссылки внутри панели не собрать).
func (h *Handlers) AdminGroupPanelAccessSet(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug     string `json:"slug"`
		Role     string `json:"role"`
		Login    string `json:"login"`
		Password string `json:"password"`
		Clear    bool   `json:"clear"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if !domain.IsValidSlug(slug) || (role != "jury" && role != "admin") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}

	var cred *domain.GroupPanelCredential
	if !req.Clear {
		login := strings.TrimSpace(req.Login)
		if login == "" || req.Password == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "укажите логин и пароль"})
			return
		}
		cred = &domain.GroupPanelCredential{Login: login, Password: req.Password}
	}
	if gf.PanelAccess == nil {
		gf.PanelAccess = &domain.GroupPanelAccess{}
	}
	if role == "admin" {
		gf.PanelAccess.Admin = cred
	} else {
		gf.PanelAccess.Jury = cred
	}
	if gf.PanelAccess.Jury == nil && gf.PanelAccess.Admin == nil {
		gf.PanelAccess = nil
	}
	// Панель ведёт на страницы группы по токену — заводим его, если не было.
	if gf.PanelConfigured() && strings.TrimSpace(gf.GroupSecretToken) == "" {
		token, err := randomHexToken()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		gf.GroupSecretToken = token
	}
	if err := h.writeGroupFile(slug, gf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// randomHexToken — 16 случайных байт в hex (как токен группы).
func randomHexToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
