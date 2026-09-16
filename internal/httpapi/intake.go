package httpapi

// Анкеты: одна очередь, две страницы поверх неё.
//
//	/standings/admin/intake            — все анкеты (админка)
//	/standings/<slug>/manage/intake    — анкеты своей группы (право intake.view)
//
// Обе страницы делают одно и то же и ходят в одно ядро (studentintake):
// показать очередь, посмотреть «что произойдёт», принять выбранные, поправить
// или убрать. Отличие ровно одно — фильтр по группе (scope): у админки он
// пустой. Поэтому хендлеры ниже параметризованы scope, а не продублированы.
//
// Права на стороне группы: смотреть — intake.view, всё остальное — intake.merge.
// Со стороны группы нельзя менять список групп анкеты и убирать её из чужих
// групп: вычёркивается только своя.

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/studentintake"
)

// IntakePageData — страница анкет (общая для админки и группы).
type IntakePageData struct {
	PageTitle string
	Footer    FooterInfo
	// Admin — админский вид: видны все анкеты и можно менять их группы.
	Admin bool
	// GroupSlug/GroupTitle/Token/RoleTitle — заполняются для страницы группы.
	GroupSlug  string
	GroupTitle string
	Token      string
	RoleTitle  string
	// APIBase — префикс ручек: /api/admin/intake или /api/group-panel/intake.
	APIBase string
	// CanAccept/CanEdit — показывать ли приём и правку.
	CanAccept bool
	CanEdit   bool
	Rows      []IntakeRow
	// RowsJSON — те же анкеты для формы правки:
	// [{full_name, public_name, groups, accounts:[{site, account_id}]}].
	RowsJSON template.JS
	// KnownGroups — слаги групп сайта для формы правки (только в админке).
	KnownGroups []string
}

// IntakeRow — одна анкета в очереди.
type IntakeRow struct {
	FullName   string
	PublicName string
	Accounts   []domain.Account
	// Groups — все группы анкеты (в админке показываются целиком).
	Groups []string
	// OtherGroups — прочие группы анкеты, кроме текущей (для страницы группы).
	OtherGroups []string
	// KnownStudent — ученик с таким ФИО уже есть в общей базе.
	KnownStudent bool
	// InGroup — ученик уже состоит в группе, из которой смотрим.
	InGroup bool
}

// intakeScope — область запроса: слаг группы для панели, пустая строка для
// админки (там фильтра по группе нет, и слаг из тела не читается вовсе).
func intakeScope(inGroup bool, slug string) string {
	if !inGroup {
		return ""
	}
	return strings.TrimSpace(slug)
}

// intakeStagingPath — файл-пачка старой схемы. Ядро вливает его в очередь при
// первом чтении; путь передаётся только ради этой миграции.
func (h *Handlers) intakeStagingPath() string {
	return h.dataPath("student_intake_admin.json")
}

// intakeRows читает очередь и готовит строки. scope != "" — только анкеты этой
// группы.
func (h *Handlers) intakeRows(scope string) ([]IntakeRow, template.JS, error) {
	queue, err := h.intake.IntakeQueue(h.intakeStagingPath())
	if err != nil {
		return nil, "", err
	}

	knownByName := make(map[string]string) // ФИО → id ученика в общей базе
	if students, err := h.loadStudentsList(); err == nil {
		for _, st := range students {
			knownByName[strings.TrimSpace(st.FullName)] = st.ID
		}
	}
	inGroup := make(map[string]struct{})
	if scope != "" {
		for _, id := range h.resolveGroupStudentIDs(scope) {
			inGroup[id] = struct{}{}
		}
	}

	rows := make([]IntakeRow, 0, len(queue))
	for _, st := range queue {
		if scope != "" && !containsSlug(st.Groups, scope) {
			continue
		}
		row := IntakeRow{
			FullName:   st.FullName,
			PublicName: st.PublicName,
			Accounts:   st.Accounts,
			Groups:     st.Groups,
		}
		if id, known := knownByName[strings.TrimSpace(st.FullName)]; known {
			row.KnownStudent = true
			if _, member := inGroup[id]; member {
				row.InGroup = true
			}
		}
		for _, g := range st.Groups {
			if !strings.EqualFold(strings.TrimSpace(g), scope) {
				row.OtherGroups = append(row.OtherGroups, strings.TrimSpace(g))
			}
		}
		rows = append(rows, row)
	}

	type entryJSON struct {
		FullName   string           `json:"full_name"`
		PublicName string           `json:"public_name"`
		Groups     []string         `json:"groups,omitempty"`
		Accounts   []domain.Account `json:"accounts,omitempty"`
	}
	entries := make([]entryJSON, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, entryJSON{
			FullName: row.FullName, PublicName: row.PublicName,
			Groups: row.Groups, Accounts: row.Accounts,
		})
	}
	blob, err := json.Marshal(entries)
	if err != nil {
		return nil, "", err
	}
	return rows, template.JS(blob), nil
}

// intakeSelection — тело запросов приёма и превью: какие анкеты берём.
type intakeSelectionRequest struct {
	Slug      string   `json:"slug"`
	FullNames []string `json:"full_names"`
}

// selection превращает тело запроса в выборку ядра. Пустой список ФИО означает
// «все подходящие» (кнопка «принять все»).
func (req intakeSelectionRequest) selection(scope string) studentintake.IntakeSelection {
	sel := studentintake.IntakeSelection{Group: scope}
	if len(req.FullNames) > 0 {
		sel.FullNames = req.FullNames
	}
	return sel
}

// requireIntake — общий вход мутирующих ручек: проверка права и настроенности
// приёма анкет. scope=="" — админка (право уже проверено AdminAuth).
func (h *Handlers) requireIntake(w http.ResponseWriter, r *http.Request, scope string, perm domain.Perm) (*GroupAccess, bool) {
	if h.intake == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "приём анкет не настроен"})
		return nil, false
	}
	if scope == "" {
		return nil, true
	}
	acc, allowed := h.requirePerm(w, r, scope, perm)
	if !allowed {
		return nil, false
	}
	// Глобальный доступ со scope=all покрывает любой слаг, а приём умеет
	// заводить группу «по дороге» (AddStudentsToGroups создаёт скелет).
	if !h.groupExists(scope) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "группа не найдена"})
		return nil, false
	}
	return acc, true
}

// intakePreview — «что произойдёт»: для каждой анкеты, кем она станет и куда
// попадёт. Ничего не пишет.
func (h *Handlers) intakePreview(w http.ResponseWriter, r *http.Request, inGroup bool) {
	var req intakeSelectionRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	scope := intakeScope(inGroup, req.Slug)
	if _, ok := h.requireIntake(w, r, scope, domain.PermIntakeMerge); !ok {
		return
	}
	preview, err := h.intake.PreviewIntake(h.admin.cfg.DataDir, h.intakeStagingPath(), req.selection(scope))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preview": preview})
}

// intakeAccept принимает выбранные анкеты; невыбранные остаются в очереди.
func (h *Handlers) intakeAccept(w http.ResponseWriter, r *http.Request, inGroup bool) {
	var req intakeSelectionRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	scope := intakeScope(inGroup, req.Slug)
	acc, ok := h.requireIntake(w, r, scope, domain.PermIntakeMerge)
	if !ok {
		return
	}
	stats, err := h.intake.AcceptIntake(h.admin.cfg.DataDir, h.intakeStagingPath(), req.selection(scope))
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	detail := "принято " + strconv.Itoa(stats.Accepted) + ", заведено " + strconv.Itoa(stats.Created)
	if scope == "" {
		h.auditAdmin(r, "intake.accept", "", detail)
	} else {
		h.auditAccessResult(r, acc, scope, "intake.accept", detail, errMsg)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errMsg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "accepted": stats.Accepted, "created": stats.Created,
		"updated": stats.Updated, "remaining": stats.Remaining,
	})
}

// intakeEntryRequest — правка одной анкеты в очереди.
type intakeEntryRequest struct {
	Slug string `json:"slug"`
	// OrigFullName — под каким ФИО анкета лежит сейчас (ключ записи).
	OrigFullName string   `json:"orig_full_name"`
	FullName     string   `json:"full_name"`
	PublicName   string   `json:"public_name"`
	Groups       []string `json:"groups"`
	Accounts     []struct {
		Site      string `json:"site"`
		AccountID string `json:"account_id"`
	} `json:"accounts"`
}

// intakeEntrySave правит анкету до приёма: опечатка в ФИО, публичное имя,
// аккаунты, а в админке — ещё и список групп.
func (h *Handlers) intakeEntrySave(w http.ResponseWriter, r *http.Request, inGroup bool) {
	var req intakeEntryRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	scope := intakeScope(inGroup, req.Slug)
	acc, ok := h.requireIntake(w, r, scope, domain.PermIntakeMerge)
	if !ok {
		return
	}

	accounts := make([]domain.Account, 0, len(req.Accounts))
	for _, a := range req.Accounts {
		accounts = append(accounts, domain.Account{Site: a.Site, AccountID: a.AccountID})
	}
	updated := domain.Student{
		FullName:   req.FullName,
		PublicName: req.PublicName,
		Accounts:   accounts,
		Groups:     req.Groups,
	}
	err := h.intake.UpdateIntakeEntry(h.intakeStagingPath(), req.OrigFullName, updated, scope)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	detail := "анкета «" + strings.TrimSpace(req.OrigFullName) + "»"
	if scope == "" {
		h.auditAdmin(r, "intake.entry.save", "", detail)
	} else {
		h.auditAccessResult(r, acc, scope, "intake.entry.save", detail, errMsg)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errMsg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// intakeEntryDiscard убирает анкету из очереди, не заводя ученика.
func (h *Handlers) intakeEntryDiscard(w http.ResponseWriter, r *http.Request, inGroup bool) {
	var req struct {
		Slug     string `json:"slug"`
		FullName string `json:"full_name"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	scope := intakeScope(inGroup, req.Slug)
	acc, ok := h.requireIntake(w, r, scope, domain.PermIntakeMerge)
	if !ok {
		return
	}
	err := h.intake.DiscardIntakeEntry(h.intakeStagingPath(), req.FullName, scope)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	detail := "анкета «" + strings.TrimSpace(req.FullName) + "»"
	if scope == "" {
		h.auditAdmin(r, "intake.entry.discard", "", detail)
	} else {
		h.auditAccessResult(r, acc, scope, "intake.entry.discard", detail, errMsg)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errMsg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Страница и ручки группы ─────────────────────────────────────────────────

// GroupIntakePage — анкеты, поданные в эту группу (право intake.view;
// приём, правка и удаление — по intake.merge).
func (h *Handlers) GroupIntakePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	acc, ok := h.signInToGroup(w, r, slug)
	if !ok {
		return
	}
	if !acc.HasAny(domain.PermIntakeView, domain.PermIntakeMerge) {
		http.Error(w, "нет права смотреть анкеты группы", http.StatusForbidden)
		return
	}
	gf, found := h.readSourceGroupFile(slug)
	if !found {
		http.NotFound(w, r)
		return
	}
	if h.intake == nil {
		http.Error(w, "приём анкет не настроен", http.StatusInternalServerError)
		return
	}
	rows, rowsJSON, err := h.intakeRows(slug)
	if err != nil {
		h.logger.Printf("ERROR read intake for group %s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	title := strings.TrimSpace(gf.Title)
	if title == "" {
		title = slug
	}
	page := IntakePageData{
		PageTitle:  title + " — анкеты",
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		Token:      acc.Token,
		RoleTitle:  acc.Title(),
		APIBase:    "/api/group-panel/intake",
		CanAccept:  acc.Has(domain.PermIntakeMerge),
		CanEdit:    acc.Has(domain.PermIntakeMerge),
		Rows:       rows,
		RowsJSON:   rowsJSON,
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := h.renderer.Render(w, http.StatusOK, "admin_intake.html", page); err != nil {
		h.logger.Printf("ERROR render group intake slug=%s: %v", slug, err)
	}
}

// PanelIntakePreview/Accept/EntrySave/EntryDiscard — то же, но в рамках группы.
func (h *Handlers) PanelIntakePreview(w http.ResponseWriter, r *http.Request) {
	h.intakePreview(w, r, true)
}

func (h *Handlers) PanelIntakeAccept(w http.ResponseWriter, r *http.Request) {
	h.intakeAccept(w, r, true)
}

func (h *Handlers) PanelIntakeEntrySave(w http.ResponseWriter, r *http.Request) {
	h.intakeEntrySave(w, r, true)
}

func (h *Handlers) PanelIntakeEntryDiscard(w http.ResponseWriter, r *http.Request) {
	h.intakeEntryDiscard(w, r, true)
}

// containsSlug — есть ли слаг в списке (без учёта регистра и пробелов).
func containsSlug(list []string, slug string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), slug) {
			return true
		}
	}
	return false
}
