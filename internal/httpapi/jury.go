package httpapi

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// Жюри-панель: операции по group_secret_token (без админского логина), строго в
// рамках своей группы. Разрешено только:
//   - добавить в группу ссылку на уже созданный глобальный контест (сверху);
//   - поменять контесты местами;
//   - выставить ручные оценки (grades_manual.json);
//   - заполнить кондуит (provider_config.table inline-контеста manual_table).
//
// Никаких inline-редактирований контестов, настроек и удалений.

// juryAuthorized проверяет slug и токен группы. Требует настроенной админки
// (жюри-операции пишут те же файлы теми же helpers).
func (h *Handlers) juryAuthorized(slug, token string) bool {
	return h.admin != nil && domain.IsValidSlug(slug) && strings.TrimSpace(token) != "" && h.groupTokenValid(slug, strings.TrimSpace(token))
}

// juryCanManageContests — у объединённой группы нет своих контестов (они
// собираются из групп-участниц), добавлять/двигать нечего.
func (h *Handlers) juryCanManageContests(slug string) bool {
	gf, ok, err := h.readGroupFile(slug)
	return err == nil && ok && len(gf.MemberGroups) == 0
}

func juryDeny(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "неверный токен группы"})
}

// JuryContestAddRef добавляет ссылку на глобальный контест в начало списка
// контестов группы (жюри, по токену).
func (h *Handlers) JuryContestAddRef(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug  string `json:"slug"`
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	id := strings.TrimSpace(req.ID)
	if !h.juryAuthorized(slug, req.Token) {
		juryDeny(w)
		return
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	if !h.juryCanManageContests(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "у объединённой группы нет своих контестов"})
		return
	}
	if status, msg := h.addGroupContestRef(slug, id); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// JuryContestMove меняет контест группы местами с соседним (жюри, по токену).
func (h *Handlers) JuryContestMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug  string `json:"slug"`
		Token string `json:"token"`
		ID    string `json:"id"`
		Dir   string `json:"dir"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	id := strings.TrimSpace(req.ID)
	dir := strings.TrimSpace(req.Dir)
	if !h.juryAuthorized(slug, req.Token) {
		juryDeny(w)
		return
	}
	if id == "" || (dir != "up" && dir != "down") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	if !h.juryCanManageContests(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "у объединённой группы нет своих контестов"})
		return
	}
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if status, msg := h.moveGroupContestEntry(slug, id, dir, entries); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// JuryGradesSave сохраняет ручные оценки группы (жюри, по токену).
func (h *Handlers) JuryGradesSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug   string                        `json:"slug"`
		Token  string                        `json:"token"`
		Grades map[string]map[string]float64 `json:"grades"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.juryAuthorized(slug, req.Token) {
		juryDeny(w)
		return
	}
	if status, msg := h.saveManualGrades(slug, req.Grades); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

const maxJuryKonduitTableBytes = 512 * 1024

// juryKonduitEntry находит inline-контест кондуита (manual_table) группы по id.
// Возвращает распакованный inline-объект и его конфиг.
func (h *Handlers) juryKonduitEntry(slug, id string) (map[string]json.RawMessage, map[string]any, bool) {
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return nil, nil, false
	}
	for _, e := range entries {
		if e.id == id {
			return h.juryKonduitEntryFromRaw(e)
		}
	}
	return nil, nil, false
}

// JuryKonduitSave обновляет таблицу оценок кондуита — только содержимое
// provider_config.table (+task_count) inline-контеста manual_table этой группы.
func (h *Handlers) JuryKonduitSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug      string `json:"slug"`
		Token     string `json:"token"`
		ID        string `json:"id"`
		Table     string `json:"table"`
		TaskCount int    `json:"task_count"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	id := strings.TrimSpace(req.ID)
	if !h.juryAuthorized(slug, req.Token) {
		juryDeny(w)
		return
	}
	if id == "" || len(req.Table) > maxJuryKonduitTableBytes || req.TaskCount < 1 || req.TaskCount > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]json.RawMessage, 0, len(entries))
	updated := false
	for _, e := range entries {
		if e.id != id {
			out = append(out, e.raw)
			continue
		}
		obj, cfg, ok := h.juryKonduitEntryFromRaw(e)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "это не кондуит группы (жюри правит только inline-контесты manual_table)"})
			return
		}
		cfg["table"] = req.Table
		cfg["task_count"] = req.TaskCount
		cfgBlob, err := json.Marshal(cfg)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		obj["provider_config"] = cfgBlob
		blob, err := json.Marshal(obj)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		out = append(out, blob)
		updated = true
	}
	if !updated {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "кондуит не найден в группе"})
		return
	}
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// juryKonduitEntryFromRaw — как juryKonduitEntry, но по уже загруженной записи.
func (h *Handlers) juryKonduitEntryFromRaw(e groupContestEntry) (map[string]json.RawMessage, map[string]any, bool) {
	if !e.inline {
		return nil, nil, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(e.raw, &obj) != nil {
		return nil, nil, false
	}
	var provider string
	_ = json.Unmarshal(obj["provider"], &provider)
	if strings.TrimSpace(provider) != source.ManualTableProviderID {
		return nil, nil, false
	}
	cfg := map[string]any{}
	if rawCfg, ok := obj["provider_config"]; ok {
		_ = json.Unmarshal(rawCfg, &cfg)
	}
	return obj, cfg, true
}

// ---- Страницы жюри ----

type JuryGradesPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	Token      string
	Columns    []AdminManualGradeColumn
	Rows       []AdminManualGradeRow
}

// JuryGradesPage — редактор ручных оценок по токену группы (только значения,
// без конструктора столбцов).
func (h *Handlers) JuryGradesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if !h.juryAuthorized(slug, token) {
		http.NotFound(w, r)
		return
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(groupFile.Title)
	if title == "" {
		title = slug
	}

	manualColumns := manualGradeColumns(groupFile)
	columns := make([]AdminManualGradeColumn, 0, len(manualColumns))
	for _, col := range manualColumns {
		colTitle := strings.TrimSpace(col.Title)
		if colTitle == "" {
			colTitle = col.ID
		}
		columns = append(columns, AdminManualGradeColumn{ID: col.ID, Title: colTitle})
	}

	publicNames := h.loadPublicNames()
	manual, err := h.loadManualGrades(slug)
	if err != nil {
		h.logger.Printf("ERROR jury grades read manual slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rows := make([]AdminManualGradeRow, 0, len(groupFile.StudentIDs))
	for _, studentID := range domain.NormalizeGroups(groupFile.StudentIDs) {
		publicName := publicNames[studentID]
		if publicName == "" {
			publicName = studentID
		}
		values := make([]string, len(manualColumns))
		for i, col := range manualColumns {
			if byStudent, ok := manual[col.ID]; ok {
				if v, ok := byStudent[studentID]; ok {
					values[i] = strconv.FormatFloat(v, 'f', -1, 64)
				}
			}
		}
		rows = append(rows, AdminManualGradeRow{StudentID: studentID, PublicName: publicName, Values: values})
	}
	sortManualGradeRows(rows)

	page := JuryGradesPageData{
		PageTitle:  "Оценки (жюри) — " + title,
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		Token:      token,
		Columns:    columns,
		Rows:       rows,
	}
	if err := h.renderer.Render(w, http.StatusOK, "jury_grades.html", page); err != nil {
		h.logger.Printf("ERROR render jury grades slug=%s err=%v", slug, err)
	}
}

type JuryKonduitPageData struct {
	PageTitle    string
	Footer       FooterInfo
	GroupSlug    string
	GroupTitle   string
	Token        string
	ContestID    string
	ContestTitle string
	Labels       []string
	LabelsJSON   template.JS
	Rows         []JuryKonduitRow
}

type JuryKonduitRow struct {
	Name string
	Vals []string
}

// JuryKonduitPage — редактор кондуита по токену группы: сетка с уже
// подставленными колонками и учениками (существующие строки + недостающие
// ученики группы).
func (h *Handlers) JuryKonduitPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !h.juryAuthorized(slug, token) || id == "" {
		http.NotFound(w, r)
		return
	}
	obj, cfg, ok := h.juryKonduitEntry(slug, id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	groupFile, okGF, err := h.readGroupFile(slug)
	if err != nil || !okGF {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(groupFile.Title)
	if title == "" {
		title = slug
	}
	contestTitle := id
	var ct string
	if json.Unmarshal(obj["title"], &ct) == nil && strings.TrimSpace(ct) != "" {
		contestTitle = strings.TrimSpace(ct)
	}

	table, _ := cfg["table"].(string)
	taskCount := 0
	if v, ok := cfg["task_count"].(float64); ok {
		taskCount = int(v)
	}
	labels, rawRows := source.SplitManualTable(table, taskCount)

	normName := func(s string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(s), "ё", "е")), " ")
	}
	rows := make([]JuryKonduitRow, 0, len(rawRows))
	seen := make(map[string]struct{}, len(rawRows))
	for _, rr := range rawRows {
		seen[normName(rr[0])] = struct{}{}
		rows = append(rows, JuryKonduitRow{Name: rr[0], Vals: rr[1:]})
	}
	// Недостающие ученики группы — пустыми строками (полное ФИО для матчинга).
	studentsByID := h.loadStudentsByID()
	for _, sid := range domain.NormalizeGroups(groupFile.StudentIDs) {
		s := studentsByID[sid]
		name := strings.TrimSpace(s.FullName)
		if name == "" {
			name = strings.TrimSpace(s.PublicName)
		}
		if name == "" {
			continue
		}
		if _, ok := seen[normName(name)]; ok {
			continue
		}
		rows = append(rows, JuryKonduitRow{Name: name, Vals: make([]string, len(labels))})
	}
	// Для удобства заполнения строки — по алфавиту ФИО.
	sort.SliceStable(rows, func(i, j int) bool { return normName(rows[i].Name) < normName(rows[j].Name) })

	labelsBlob, err := json.Marshal(labels)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	page := JuryKonduitPageData{
		PageTitle:    "Кондуит — " + contestTitle,
		Footer:       h.buildFooterInfo(),
		GroupSlug:    slug,
		GroupTitle:   title,
		Token:        token,
		ContestID:    id,
		ContestTitle: contestTitle,
		Labels:       labels,
		LabelsJSON:   template.JS(labelsBlob),
		Rows:         rows,
	}
	if err := h.renderer.Render(w, http.StatusOK, "jury_konduit.html", page); err != nil {
		h.logger.Printf("ERROR render jury konduit slug=%s id=%s err=%v", slug, id, err)
	}
}

// juryStandingsExtras заполняет данные жюри-панели страницы группы (по
// валидному токену): глобальные контесты для добавления, кондуиты для
// заполнения, наличие ручных оценок.
func (h *Handlers) juryStandingsExtras(slug string, page *GroupPageData) {
	if h.admin == nil {
		return
	}
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		return
	}
	page.JuryHasGrades = len(manualGradeColumns(gf)) > 0

	if len(gf.MemberGroups) > 0 {
		return // объединённая группа: своих контестов нет
	}
	page.JuryCanManage = true

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return
	}
	inGroup := make(map[string]struct{}, len(entries))
	konduits := make(map[string]bool)
	for _, e := range entries {
		inGroup[e.id] = struct{}{}
		if _, _, ok := h.juryKonduitEntryFromRaw(e); ok {
			konduits[e.id] = true
		}
	}
	page.JuryKonduits = konduits

	globals, err := h.loadContestsList()
	if err != nil {
		return
	}
	for _, c := range globals {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		if _, ok := inGroup[id]; ok {
			continue
		}
		t := strings.TrimSpace(c.Title)
		if t == "" {
			t = id
		}
		opt := AdminGroupContestOption{ID: id, Title: t}
		if kind, isProvider := contestKindLabel(c); isProvider {
			opt.Kind = kind
		}
		page.JuryAddable = append(page.JuryAddable, opt)
	}
}
