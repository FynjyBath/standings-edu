package httpapi

// Страницы и API управления группой для её доступов (см. access_resolve.go).
// Каждая операция требует своего права; кто пришёл — определяется токеном в
// адресе или кукой сессии, выданной входом по логину и паролю.
//
// Логика операций не дублируется: панель зовёт те же внутренние helpers, что и
// админка (addGroupContestRef, moveGroupContestEntry, saveInlineContest…), а
// разметка управления контестами — общий partial groupContests.

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"standings-edu/internal/domain"
)

// GroupPanelContestsPageData — страница управления контестами в панели.
type GroupPanelContestsPageData struct {
	AdminGroupManagePageData
	// RoleTitle — подпись доступа в шапке страницы.
	RoleTitle string
	// GroupToken — токен группы для ссылок «страница группы» из панели.
	GroupToken string
}

// GroupManageContestsPage — управление контестами группы (право
// contests.manage): та же таблица и та же форма контеста, что в админке.
func (h *Handlers) GroupManageContestsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	acc, ok := h.signInToGroup(w, r, slug)
	if !ok {
		return
	}
	if !acc.Has(domain.PermContestsManage) {
		http.Error(w, "нет права управлять контестами", http.StatusForbidden)
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
	// Редактор доступов — только в админке: тут его не рисуют, а секреты в
	// данных страницы держать незачем.
	base.Accesses = AccessEditorData{}
	// Общий список контестов сайта отдаём только по праву: без него это ещё и
	// утечка названий чужих контестов.
	base.CanAddGlobal = acc.Has(domain.PermContestsGlobal)
	if !base.CanAddGlobal {
		base.AddableContests = nil
	}
	base.CanEditInline = acc.Has(domain.PermContestsInline)
	w.Header().Set("Cache-Control", "no-store")
	page := GroupPanelContestsPageData{
		AdminGroupManagePageData: base,
		RoleTitle:                acc.Title(),
		GroupToken:               acc.Token,
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_panel_contests.html", page); err != nil {
		h.logger.Printf("ERROR render panel contests slug=%s: %v", slug, err)
	}
}

// GroupManageGradesPage — редактор оценок группы: настройка столбцов (веса,
// метрика, нормировка, коэффициенты) и сетка ручных оценок. Тот же шаблон, что
// в админке; блоки показываются по правам.
func (h *Handlers) GroupManageGradesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	acc, ok := h.signInToGroup(w, r, slug)
	if !ok {
		return
	}
	if !acc.HasAny(domain.PermGradesManual, domain.PermGradesConfig) {
		http.Error(w, "нет права на оценки", http.StatusForbidden)
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
	page.RoleTitle = acc.Title()
	page.GroupToken = acc.Token
	page.APIBase = "/api/group-panel"
	page.CanEditConfig = acc.Has(domain.PermGradesConfig)
	page.CanEditGrades = acc.Has(domain.PermGradesManual)
	page.BackURL = "/standings/" + slug + h.tokenQuery(acc.Token)
	page.BackLabel = "← Таблицы группы"
	w.Header().Set("Cache-Control", "no-store")
	if err := h.renderer.Render(w, http.StatusOK, "admin_group_grades.html", page); err != nil {
		h.logger.Printf("ERROR render panel grades slug=%s: %v", slug, err)
	}
}

// ── API управления группой ───────────────────────────────────────────────────
// Пути зеркалят админские (/api/admin/group/... → /api/group-panel/...), тела
// запросов те же; права проверяются по доступу запроса.

// panelContestRequest — общая часть тел запросов по контестам группы.
type panelContestRequest struct {
	Slug string `json:"slug"`
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
	acc, allowed := h.requirePerm(w, r, slug, domain.PermContestsManage)
	if !allowed {
		return
	}
	// Добавление ссылки — это доступ к общему списку контестов сайта, отдельное
	// право: без него доступ работает только со своими (inline) контестами.
	if !acc.Has(domain.PermContestsGlobal) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "нет доступа к общему списку контестов сайта"})
		return
	}
	status, msg := h.addGroupContestRef(slug, strings.TrimSpace(req.ID))
	h.auditAccessResult(r, acc, slug, "contests.add-ref", "id="+strings.TrimSpace(req.ID), msg)
	if msg != "" {
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
	acc, allowed := h.requirePerm(w, r, slug, domain.PermContestsManage)
	if !allowed {
		return
	}
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	status, msg := h.moveGroupContestEntry(slug, strings.TrimSpace(req.ID), strings.TrimSpace(req.Dir), entries)
	h.auditAccessResult(r, acc, slug, "contests.move", "id="+strings.TrimSpace(req.ID)+" "+strings.TrimSpace(req.Dir), msg)
	if msg != "" {
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
	acc, allowed := h.requirePerm(w, r, slug, domain.PermContestsManage)
	if !allowed {
		return
	}
	status, msg := h.removeGroupContestEntry(slug, strings.TrimSpace(req.ID))
	h.auditAccessResult(r, acc, slug, "contests.remove", "id="+strings.TrimSpace(req.ID), msg)
	if msg != "" {
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
	acc, allowed := h.requirePerm(w, r, slug, domain.PermContestsManage)
	if !allowed {
		return
	}
	status, msg := h.setGroupContestOptions(slug, req)
	h.auditAccessResult(r, acc, slug, "contests.set-options", "id="+strings.TrimSpace(req.ID), msg)
	if msg != "" {
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
		Slug string `json:"slug"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermContestsInline)
	if !allowed {
		return
	}
	// Править из группы можно только свои (inline) записи: подмена ссылки на
	// глобальный контест своим одноимённым контестом молча отцепила бы группу
	// от общего определения.
	if origID := strings.TrimSpace(req.OriginalID); origID != "" {
		entries, err := h.loadGroupContestEntries(slug)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		for _, e := range entries {
			if e.id == origID && !e.inline {
				writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "это ссылка на глобальный контест — из группы её не изменить"})
				return
			}
		}
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
	h.auditAccessResult(r, acc, slug, "contests.inline-save", "id="+strings.TrimSpace(req.ID), msg)
	if msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// PanelGradesSave — ручные оценки группы (роль «Жюри»).
func (h *Handlers) PanelGradesSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug   string                        `json:"slug"`
		Grades map[string]map[string]float64 `json:"grades"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermGradesManual)
	if !allowed {
		return
	}
	status, msg := h.saveManualGrades(slug, req.Grades)
	h.auditAccessResult(r, acc, slug, "grades.manual.save", "учеников "+strconv.Itoa(len(req.Grades)), msg)
	if msg != "" {
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
	acc, allowed := h.requirePerm(w, r, slug, domain.PermGradesConfig)
	if !allowed {
		return
	}
	status, msg := h.saveGradesConfig(slug, req)
	h.auditAccessResult(r, acc, slug, "grades.config.save", "столбцов "+strconv.Itoa(len(req.Columns)), msg)
	if msg != "" {
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

// randomHexToken — 16 случайных байт в hex (как токен группы).
func randomHexToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
