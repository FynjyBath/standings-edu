package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// Операции жюри: по role-token'у панели группы (роль «Жюри» и выше), строго в
// рамках своей группы:
//   - выставить ручные оценки и настроить таблицу оценок (см. group_panel.go);
//   - создать кондуит и заполнить его (manual_table);
//   - разметить флаги нечестности.
//
// Управление контестами — роль «Админ», см. group_panel.go.

// juryAuthorized — права жюри по role-token'у панели (см. group_access.go).
// Токена группы (?token=) для операций НЕДОСТАТОЧНО: он даёт только просмотр.
func (h *Handlers) juryAuthorized(slug, roleToken string) bool {
	return h.admin != nil && h.groupRole(slug, "", roleToken).AtLeast(RoleJury)
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

// JuryKonduitCreate создаёт новый кондуит группы (жюри, по токену): только
// название и число задач. Контест всегда inline manual_table (edu, плюсики),
// id генерируется автоматически, добавляется в начало списка. Оценки жюри
// заполняет затем в редакторе кондуита.
func (h *Handlers) JuryKonduitCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug      string `json:"slug"`
		RoleToken string `json:"role_token"`
		Title     string `json:"title"`
		TaskCount int    `json:"task_count"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	title := strings.TrimSpace(req.Title)
	if !h.juryAuthorized(slug, req.RoleToken) {
		juryDeny(w)
		return
	}
	if title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "укажите название кондуита"})
		return
	}
	if req.TaskCount < 1 || req.TaskCount > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "число задач должно быть от 1 до 200"})
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
	taken := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		taken[e.id] = struct{}{}
	}
	id, err := generateKonduitID(taken)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	encoded, err := json.Marshal(map[string]any{
		"id":              id,
		"title":           title,
		"score_system":    "edu",
		"source_type":     domain.ContestTypeProvider,
		"provider":        source.ManualTableProviderID,
		"provider_config": map[string]any{"task_count": req.TaskCount},
		"subcontests":     []any{},
		"update":          true,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]json.RawMessage, 0, len(entries)+1)
	out = append(out, encoded)
	for _, e := range entries {
		out = append(out, e.raw)
	}
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// generateKonduitID — уникальный id нового кондуита: konduit-<hex>.
func generateKonduitID(taken map[string]struct{}) (string, error) {
	for range 20 {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		id := "konduit-" + hex.EncodeToString(buf)
		if _, dup := taken[id]; !dup {
			return id, nil
		}
	}
	return "", errors.New("не удалось подобрать уникальный id")
}

const maxJuryKonduitTableBytes = 512 * 1024

// juryKonduit — кондуит группы, доступный жюри: inline-контест manual_table
// (правится в contests.json группы) либо ссылка на глобальный manual_table
// (правится в глобальном contests.json — так оценки общие для всех групп,
// подключивших тот же контест).
type juryKonduit struct {
	Inline bool
	Title  string
	Config map[string]any
}

// resolveJuryKonduit находит кондуит по id среди контестов группы.
func (h *Handlers) resolveJuryKonduit(slug, id string) (juryKonduit, bool) {
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return juryKonduit{}, false
	}
	for _, e := range entries {
		if e.id != id {
			continue
		}
		if e.inline {
			obj, cfg, ok := h.juryKonduitEntryFromRaw(e)
			if !ok {
				return juryKonduit{}, false
			}
			title := id
			var t string
			if json.Unmarshal(obj["title"], &t) == nil && strings.TrimSpace(t) != "" {
				title = strings.TrimSpace(t)
			}
			// Таблица — из manual_tables.json группы (легаси-конфиг как fallback).
			cfg["table"] = manualTableFor(h.groupManualTablesPath(slug), id, cfg)
			return juryKonduit{Inline: true, Title: title, Config: cfg}, true
		}
		// Ссылка: глобальное определение manual_table.
		c, ok := h.globalManualTableContest(id)
		if !ok {
			return juryKonduit{}, false
		}
		cfg := map[string]any{}
		_ = json.Unmarshal(c.ProviderConfig, &cfg)
		cfg["table"] = manualTableFor(h.globalManualTablesPath(), id, cfg)
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = id
		}
		return juryKonduit{Inline: false, Title: title, Config: cfg}, true
	}
	return juryKonduit{}, false
}

// globalManualTableContest находит глобальный контест-кондуит по id.
func (h *Handlers) globalManualTableContest(id string) (domain.Contest, bool) {
	globals, err := h.loadContestsList()
	if err != nil {
		return domain.Contest{}, false
	}
	for _, c := range globals {
		if strings.TrimSpace(c.ID) == id && strings.TrimSpace(c.Provider) == source.ManualTableProviderID {
			return c, true
		}
	}
	return domain.Contest{}, false
}

// JuryKonduitSave обновляет таблицу оценок кондуита — только содержимое
// provider_config.table (+task_count). Для inline-контеста — в contests.json
// группы; для ссылки на глобальный кондуит — в глобальном contests.json (один
// контест на несколько групп: оценки общие).
func (h *Handlers) JuryKonduitSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug      string `json:"slug"`
		RoleToken string `json:"role_token"`
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
	if !h.juryAuthorized(slug, req.RoleToken) {
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
	for _, e := range entries {
		if e.id != id {
			continue
		}
		var status int
		var msg string
		if e.inline {
			status, msg = h.juryKonduitSaveInline(slug, id, req.Table, req.TaskCount, entries)
		} else {
			status, msg = h.juryKonduitSaveGlobal(slug, id, req.Table, req.TaskCount)
		}
		if msg != "" {
			writeJSON(w, status, map[string]any{"ok": false, "error": msg})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "кондуит не найден в группе"})
}

// juryKonduitSaveInline пишет таблицу inline-кондуита группы в её
// manual_tables.json; в определении контеста обновляется только task_count
// (легаси-таблица из provider_config при этом убирается — миграция).
func (h *Handlers) juryKonduitSaveInline(slug, id, table string, taskCount int, entries []groupContestEntry) (int, string) {
	out := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		if e.id != id {
			out = append(out, e.raw)
			continue
		}
		obj, cfg, ok := h.juryKonduitEntryFromRaw(e)
		if !ok {
			return http.StatusBadRequest, "это не кондуит (manual_table)"
		}
		delete(cfg, "table")
		cfg["task_count"] = taskCount
		cfgBlob, err := json.Marshal(cfg)
		if err != nil {
			return http.StatusInternalServerError, err.Error()
		}
		obj["provider_config"] = cfgBlob
		blob, err := json.Marshal(obj)
		if err != nil {
			return http.StatusInternalServerError, err.Error()
		}
		out = append(out, blob)
	}
	if err := setManualTablesEntry(h.groupManualTablesPath(slug), id, table); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return http.StatusOK, ""
}

// juryKonduitSaveGlobal пишет оценки группы в общий (глобальный) кондуит.
// Жюри редактирует только строки СВОИХ учеников: присланная таблица считается
// полной правдой для учеников группы, а строки чужих групп из сохранённой
// таблицы переносятся без изменений. Остальные поля контеста и конфига
// (show_all и т.п.) не трогаются.
func (h *Handlers) juryKonduitSaveGlobal(slug, id, table string, taskCount int) (int, string) {
	contests, err := h.loadContestsList()
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	for i := range contests {
		if strings.TrimSpace(contests[i].ID) != id {
			continue
		}
		if strings.TrimSpace(contests[i].Provider) != source.ManualTableProviderID {
			return http.StatusBadRequest, "это не кондуит (manual_table)"
		}
		cfg := map[string]any{}
		if len(contests[i].ProviderConfig) > 0 {
			_ = json.Unmarshal(contests[i].ProviderConfig, &cfg)
		}
		existing := manualTableFor(h.globalManualTablesPath(), id, cfg)
		merged := mergeKonduitTables(table, existing, taskCount, h.groupStudents(slug))
		// Таблица — в manual_tables.json; в определении только task_count
		// (легаси-таблица из конфига убирается — миграция).
		delete(cfg, "table")
		cfg["task_count"] = taskCount
		cfgBlob, err := json.Marshal(cfg)
		if err != nil {
			return http.StatusInternalServerError, err.Error()
		}
		contests[i].ProviderConfig = cfgBlob
		if err := setManualTablesEntry(h.globalManualTablesPath(), id, merged); err != nil {
			return http.StatusInternalServerError, err.Error()
		}
		if err := h.saveContests(contests); err != nil {
			return http.StatusInternalServerError, err.Error()
		}
		return http.StatusOK, ""
	}
	return http.StatusBadRequest, "глобальный контест не найден"
}

// groupStudents — ученики группы (для фильтрации строк общего кондуита).
func (h *Handlers) groupStudents(slug string) []domain.Student {
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		return nil
	}
	byID := h.loadStudentsByID()
	out := make([]domain.Student, 0, len(gf.StudentIDs))
	for _, sid := range domain.NormalizeGroups(gf.StudentIDs) {
		if s, ok := byID[sid]; ok {
			out = append(out, s)
		}
	}
	return out
}

// normKonduitName — нормализация имени строки кондуита для дедупликации
// (едина с матчером ФИО и слиянием таблиц в source).
func normKonduitName(s string) string {
	return source.NormalizeName(s)
}

// mergeKonduitTables сливает присланную жюри таблицу (только ученики его
// группы) с сохранённой общей: строки учеников группы берутся из присланной
// (включая удаление — отсутствие строки очищает оценки), строки чужих групп
// сохраняются как были. Заголовок — из присланной таблицы.
func mergeKonduitTables(incoming, existing string, taskCount int, students []domain.Student) string {
	labels, incomingRows := source.SplitManualTable(incoming, taskCount)
	_, existingRows := source.SplitManualTable(existing, taskCount)

	incomingNames := make(map[string]struct{}, len(incomingRows))
	for _, r := range incomingRows {
		incomingNames[normKonduitName(r[0])] = struct{}{}
	}

	// Какие строки старой таблицы принадлежат ученикам этой группы.
	existingNames := make([]string, len(existingRows))
	for i, r := range existingRows {
		existingNames[i] = r[0]
	}
	mine := source.MatchNamesToStudents(existingNames, students)

	lines := []string{"ФИО\t" + strings.Join(labels, "\t")}
	appendRow := func(r []string) { lines = append(lines, strings.Join(r, "\t")) }
	for _, r := range incomingRows {
		appendRow(r)
	}
	for i, r := range existingRows {
		if _, isMine := mine[i]; isMine {
			continue // строка ученика группы: правда — в присланной таблице
		}
		if _, dup := incomingNames[normKonduitName(r[0])]; dup {
			continue
		}
		appendRow(r)
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
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

type JuryKonduitPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	// RoleToken — подпись роли жюри для сохранения кондуита из панели.
	RoleToken    string
	ContestID    string
	ContestTitle string
	Labels       []string
	LabelsJSON   template.JS
	Rows         []JuryKonduitRow
	// Shared — глобальный кондуит по ссылке: оценки общие для всех групп,
	// подключивших этот контест.
	Shared bool
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
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	role, roleToken, authorized := h.authorizePanelRequest(w, r, slug)
	if !authorized {
		return
	}
	if !role.AtLeast(RoleJury) || id == "" {
		http.NotFound(w, r)
		return
	}
	konduit, ok := h.resolveJuryKonduit(slug, id)
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
	contestTitle := konduit.Title

	table, _ := konduit.Config["table"].(string)
	taskCount := 0
	if v, ok := konduit.Config["task_count"].(float64); ok {
		taskCount = int(v)
	}
	labels, rawRows := source.SplitManualTable(table, taskCount)

	// Общий кондуит (по ссылке): показываем только строки учеников ЭТОЙ
	// группы — чужие группы редактируют своих, их строки не трогаются.
	students := h.groupStudents(slug)
	if !konduit.Inline {
		names := make([]string, len(rawRows))
		for i, rr := range rawRows {
			names[i] = rr[0]
		}
		mine := source.MatchNamesToStudents(names, students)
		kept := make([][]string, 0, len(mine))
		for i, rr := range rawRows {
			if _, ok := mine[i]; ok {
				kept = append(kept, rr)
			}
		}
		rawRows = kept
	}

	rows := make([]JuryKonduitRow, 0, len(rawRows))
	seen := make(map[string]struct{}, len(rawRows))
	for _, rr := range rawRows {
		seen[normKonduitName(rr[0])] = struct{}{}
		rows = append(rows, JuryKonduitRow{Name: rr[0], Vals: rr[1:]})
	}
	// Недостающие ученики группы — пустыми строками (полное ФИО для матчинга).
	for _, s := range students {
		name := strings.TrimSpace(s.FullName)
		if name == "" {
			name = strings.TrimSpace(s.PublicName)
		}
		if name == "" {
			continue
		}
		if _, ok := seen[normKonduitName(name)]; ok {
			continue
		}
		rows = append(rows, JuryKonduitRow{Name: name, Vals: make([]string, len(labels))})
	}
	// Для удобства заполнения строки — по алфавиту ФИО.
	sort.SliceStable(rows, func(i, j int) bool { return normKonduitName(rows[i].Name) < normKonduitName(rows[j].Name) })

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
		RoleToken:    roleToken,
		ContestID:    id,
		ContestTitle: contestTitle,
		Labels:       labels,
		LabelsJSON:   template.JS(labelsBlob),
		Rows:         rows,
		Shared:       !konduit.Inline,
	}
	if err := h.renderer.Render(w, http.StatusOK, "jury_konduit.html", page); err != nil {
		h.logger.Printf("ERROR render jury konduit slug=%s id=%s err=%v", slug, id, err)
	}
}

// juryStandingsExtras заполняет данные жюри-панели страницы группы (по
// валидному токену): глобальные контесты для добавления, кондуиты для
// заполнения, наличие ручных оценок.
func (h *Handlers) juryStandingsExtras(slug string, page *GroupPageData, role GroupRole) {
	if h.admin == nil || !role.AtLeast(RoleJury) {
		return // наблюдателю панельные блоки не нужны
	}
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		return
	}
	page.JuryHasGrades = len(manualGradeColumns(gf)) > 0

	if len(gf.MemberGroups) > 0 {
		return // объединённая группа: своих контестов нет
	}
	// Управление контестами — только роль «админ»; жюри видит кондуиты.
	page.CanManageContests = role.AtLeast(RoleAdmin)

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return
	}
	globals, err := h.loadContestsList()
	if err != nil {
		return
	}
	globalManual := make(map[string]bool)
	for _, c := range globals {
		if strings.TrimSpace(c.Provider) == source.ManualTableProviderID {
			globalManual[strings.TrimSpace(c.ID)] = true
		}
	}

	inGroup := make(map[string]struct{}, len(entries))
	konduits := make(map[string]bool)
	konduitTitles := make(map[string]string)
	for _, e := range entries {
		inGroup[e.id] = struct{}{}
		if obj, _, ok := h.juryKonduitEntryFromRaw(e); ok {
			konduits[e.id] = true // inline-кондуит группы
			var t string
			if json.Unmarshal(obj["title"], &t) == nil && strings.TrimSpace(t) != "" {
				konduitTitles[e.id] = strings.TrimSpace(t)
			}
		} else if !e.inline && globalManual[e.id] {
			konduits[e.id] = true // ссылка на глобальный кондуит (оценки общие)
		}
	}
	page.JuryKonduits = konduits

	// Кондуиты, ещё не попавшие в сгенерированные таблицы (только созданы):
	// ссылка на редактор — из панели, ведь секции контеста на странице нет.
	generated := make(map[string]struct{}, len(page.Standings.Contests))
	for _, c := range page.Standings.Contests {
		generated[c.ID] = struct{}{}
	}
	for _, e := range entries {
		if !konduits[e.id] {
			continue
		}
		if _, ok := generated[e.id]; ok {
			continue
		}
		title := konduitTitles[e.id]
		if title == "" {
			title = e.id
		}
		page.JuryNewKonduits = append(page.JuryNewKonduits, AdminGroupContestOption{ID: e.id, Title: title})
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
