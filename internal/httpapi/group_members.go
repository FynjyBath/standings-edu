package httpapi

// Состав группы и действия над ней для доступов группы (см. access_resolve.go).
//
// Ученик — запись общая для всего сайта, поэтому права разделены:
//   - members.manage  — распоряжаться составом СВОЕЙ группы: добавить уже
//     заведённого ученика, убрать из группы. Запись ученика не создаётся и не
//     удаляется, чужие группы не задеваются;
//   - members.register — плюс заводить новых учеников (запись в общей базе);
//   - students.edit   — править данные ученика своей группы;
//   - intake.merge    — принимать анкеты своей группы.
//
// Добавление идёт ПО ФИО, а не выбором из списка: полный список учеников сайта
// доступу группы не показывается (это утечка имён чужих групп — та же причина,
// по которой общий список контестов закрыт правом contests.global).

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"standings-edu/internal/domain"
)

// GroupMembersPageData — страница «Состав группы» в панели.
type GroupMembersPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	// GroupToken — токен доступа для ссылок со страницы (пусто — вошли сессией).
	GroupToken string
	RoleTitle  string
	Members    []AdminGroupMember
	// MembersJSON — те же участники с аккаунтами, для формы правки ученика:
	// [{id, full_name, public_name, accounts:[{site, account_id}]}].
	MembersJSON template.JS
	// CanRegister — можно заводить новых учеников (право members.register).
	// Без него незнакомые ФИО в списке помечаются как пропущенные.
	CanRegister bool
	// CanEditStudents — можно править ФИО/имя/аккаунты участников.
	CanEditStudents bool
}

// GroupManageMembersPage — состав группы: список участников и форма добавления
// по списку ФИО (право members.manage).
func (h *Handlers) GroupManageMembersPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	acc, ok := h.signInToGroup(w, r, slug)
	if !ok {
		return
	}
	if !acc.Has(domain.PermMembersManage) {
		http.Error(w, "нет права управлять составом группы", http.StatusForbidden)
		return
	}
	groupFile, found, err := h.readGroupFile(slug)
	if err != nil {
		h.logger.Printf("ERROR panel members slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(groupFile.Title)
	if title == "" {
		title = slug
	}

	studentsByID := h.loadStudentsByID()
	members := make([]AdminGroupMember, 0, len(groupFile.StudentIDs))
	type memberJSON struct {
		ID         string           `json:"id"`
		FullName   string           `json:"full_name"`
		PublicName string           `json:"public_name"`
		Accounts   []domain.Account `json:"accounts,omitempty"`
	}
	details := make([]memberJSON, 0, len(groupFile.StudentIDs))
	for _, sid := range domain.NormalizeGroups(groupFile.StudentIDs) {
		s := studentsByID[sid]
		name := strings.TrimSpace(s.PublicName)
		if name == "" {
			name = sid
		}
		members = append(members, AdminGroupMember{StudentID: sid, PublicName: name, FullName: strings.TrimSpace(s.FullName)})
		details = append(details, memberJSON{
			ID: sid, FullName: strings.TrimSpace(s.FullName),
			PublicName: strings.TrimSpace(s.PublicName), Accounts: s.Accounts,
		})
	}
	membersJSON, err := json.Marshal(details)
	if err != nil {
		h.logger.Printf("ERROR panel members json slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	page := GroupMembersPageData{
		PageTitle:       "Состав: " + title,
		Footer:          h.buildFooterInfo(),
		GroupSlug:       slug,
		GroupTitle:      title,
		GroupToken:      acc.Token,
		RoleTitle:       acc.Title(),
		Members:         members,
		MembersJSON:     template.JS(membersJSON),
		CanRegister:     acc.Has(domain.PermMembersRegister),
		CanEditStudents: acc.Has(domain.PermStudentsEdit),
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := h.renderer.Render(w, http.StatusOK, "group_members.html", page); err != nil {
		h.logger.Printf("ERROR render panel members slug=%s: %v", slug, err)
	}
}

// PanelMemberRemove — убрать участника из состава группы.
func (h *Handlers) PanelMemberRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug      string `json:"slug"`
		StudentID string `json:"student_id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermMembersManage)
	if !allowed {
		return
	}
	studentID := strings.TrimSpace(req.StudentID)
	status, msg := h.removeGroupMember(slug, studentID)
	h.auditAccessResult(r, acc, slug, "members.remove", "ученик "+studentID, msg)
	if msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// panelRegisterNames — превью/применение списка ФИО из панели. Заводить новых
// учеников можно только с правом members.register; иначе незнакомые ФИО
// пропускаются с пояснением.
func (h *Handlers) panelRegisterNames(w http.ResponseWriter, r *http.Request, apply bool) {
	var req struct {
		Slug  string `json:"slug"`
		Names string `json:"names"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermMembersManage)
	if !allowed {
		return
	}
	allowCreate := acc.Has(domain.PermMembersRegister)
	plan, status, msg := h.registerGroupNames(slug, req.Names, apply, allowCreate)
	if apply {
		h.auditAccessResult(r, acc, slug, "members.register",
			"добавлено "+strconv.Itoa(plan.Add)+", заведено "+strconv.Itoa(plan.Create), msg)
	}
	if msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": plan, "applied": apply})
}

// PanelRegisterNamesDryRun — превью списка ФИО (без записи).
func (h *Handlers) PanelRegisterNamesDryRun(w http.ResponseWriter, r *http.Request) {
	h.panelRegisterNames(w, r, false)
}

// PanelRegisterNamesApply — применение списка ФИО к составу группы.
func (h *Handlers) PanelRegisterNamesApply(w http.ResponseWriter, r *http.Request) {
	h.panelRegisterNames(w, r, true)
}

// PanelStudentSave — правка данных ученика своей группы (право students.edit).
// Править можно только участников этой группы и только существующих: заводить
// новых — отдельное право (members.register, через список ФИО) или админка.
func (h *Handlers) PanelStudentSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		adminStudentSaveRequest
		Slug string `json:"slug"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermStudentsEdit)
	if !allowed {
		return
	}
	// Скоуп: только состав своей группы (у объединённой — состав участниц).
	ownStudents := make(map[string]struct{})
	for _, id := range h.resolveGroupStudentIDs(slug) {
		ownStudents[id] = struct{}{}
	}
	savedID, status, msg := h.saveStudentRecord(req.adminStudentSaveRequest, ownStudents)
	h.auditAccessResult(r, acc, slug, "students.edit", "ученик "+strings.TrimSpace(req.ID), msg)
	if msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": savedID})
}

// PanelIntakeMerge — принять анкеты своей группы (право intake.merge):
// ученики заводятся или находятся по ФИО и добавляются в состав группы.
// full_names пустой — принять все анкеты группы.
func (h *Handlers) PanelIntakeMerge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug      string   `json:"slug"`
		FullNames []string `json:"full_names"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermIntakeMerge)
	if !allowed {
		return
	}
	if h.intake == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "приём анкет не настроен"})
		return
	}
	// Глобальный доступ со scope=all покрывает любой слаг, а приём анкет умеет
	// заводить группу «по дороге» (AddStudentsToGroups создаёт скелет). Создание
	// групп — не это право, поэтому по несуществующему слагу отказываем.
	if !h.groupExists(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "группа не найдена"})
		return
	}
	// Заводить учеников умеет только merge — но добавляет он их строго в свою
	// группу, поэтому отдельного members.* здесь не требуем.
	var names []string
	if len(req.FullNames) > 0 {
		names = req.FullNames
	}
	stats, err := h.intake.MergeGroupIntake(h.admin.cfg.DataDir, h.dataPath("student_intake_admin.json"), slug, names)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	h.auditAccessResult(r, acc, slug, "intake.merge",
		"принято "+strconv.Itoa(stats.Accepted)+", заведено "+strconv.Itoa(stats.Created), errMsg)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errMsg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "accepted": stats.Accepted, "created": stats.Created,
		"updated": stats.Updated, "remaining": stats.Remaining,
	})
}

// PanelActionGenerate — пересобрать таблицы своей группы (право
// actions.generate). Запускается тот же bin/generate, что и в админке, но с
// -group: чужие таблицы доступ не трогает. Параллельные запуски отсекает общий
// для всех действий мьютекс (runAdminAction).
func (h *Handlers) PanelActionGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug string `json:"slug"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermActionsGenerate)
	if !allowed {
		return
	}
	if !h.groupExists(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "группа не найдена"})
		return
	}
	result := h.runAdminAction("generate", func() AdminActionResult {
		return h.executeGenerateAction(false, slug)
	})
	h.auditAccess(r, acc, slug, "actions.generate", "успех: "+strconv.FormatBool(result.Success))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": result.Success, "output": result.Output, "error": actionError(result),
		"duration": result.Duration,
	})
}

// PanelActionResetCache — сбросить кеш источников по своей группе (право
// actions.reset_cache). Область всегда «группа»: чужие аккаунты недоступны.
func (h *Handlers) PanelActionResetCache(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug   string `json:"slug"`
		Period string `json:"period"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	acc, allowed := h.requirePerm(w, r, slug, domain.PermActionsResetCache)
	if !allowed {
		return
	}
	if !h.groupExists(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "группа не найдена"})
		return
	}
	result := h.runAdminAction("reset_cache", func() AdminActionResult {
		return h.executeResetCacheAction(strings.TrimSpace(req.Period), "group", slug)
	})
	h.auditAccess(r, acc, slug, "actions.reset_cache", "период "+strings.TrimSpace(req.Period))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": result.Success, "output": result.Output, "error": actionError(result),
	})
}

// actionError — текст ошибки действия для ответа панели ("" — успех). Ответ
// отдаётся с кодом 200 (действие приняли и выполнили), поэтому причина неудачи
// должна приехать в теле, иначе на странице будет безликое «HTTP 200».
func actionError(result AdminActionResult) string {
	if result.Success {
		return ""
	}
	if len(result.Errors) > 0 {
		return strings.Join(result.Errors, "; ")
	}
	return "действие не удалось"
}

// groupExists — у группы есть group.json (защита от действий по чужому слагу).
func (h *Handlers) groupExists(slug string) bool {
	_, ok := h.readSourceGroupFile(slug)
	return ok
}
