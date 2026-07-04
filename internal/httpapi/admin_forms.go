package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/studentintake"
)

// ---- общие помощники доступа к данным (для form-редакторов админки) ----

func (h *Handlers) dataPath(parts ...string) string {
	return filepath.Join(append([]string{h.admin.cfg.DataDir}, parts...)...)
}

func (h *Handlers) loadContestsList() ([]domain.Contest, error) {
	var contests []domain.Contest
	if err := fileutil.ReadJSON(h.dataPath("contests.json"), &contests); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return contests, nil
}

func (h *Handlers) loadStudentsList() ([]domain.Student, error) {
	students, err := studentintake.LoadStudentsFile(h.dataPath("students.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return students, nil
}

func decodeAdminJSON(r *http.Request, out any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminJSONBodyBytes))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	return nil
}

// ---- Ученики ----

type AdminStudentsPageData struct {
	PageTitle    string
	Footer       FooterInfo
	Students     []AdminStudentView
	StudentsJSON template.JS
}

type AdminStudentView struct {
	ID         string
	FullName   string
	PublicName string
	Accounts   []domain.Account
	Groups     []string
}

type adminStudentJSON struct {
	ID         string           `json:"id"`
	FullName   string           `json:"full_name"`
	PublicName string           `json:"public_name"`
	Accounts   []domain.Account `json:"accounts"`
}

func (h *Handlers) AdminStudentsPage(w http.ResponseWriter, _ *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	students, err := h.loadStudentsList()
	if err != nil {
		h.logger.Printf("ERROR admin students load: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	normalized := domain.NormalizeStudents(students)
	sort.SliceStable(normalized, func(i, j int) bool {
		return strings.ToLower(normalized[i].PublicName) < strings.ToLower(normalized[j].PublicName)
	})
	views := make([]AdminStudentView, 0, len(normalized))
	blobItems := make([]adminStudentJSON, 0, len(normalized))
	for _, s := range normalized {
		views = append(views, AdminStudentView{
			ID: s.ID, FullName: s.FullName, PublicName: s.PublicName,
			Accounts: s.Accounts, Groups: s.Groups,
		})
		blobItems = append(blobItems, adminStudentJSON{ID: s.ID, FullName: s.FullName, PublicName: s.PublicName, Accounts: s.Accounts})
	}
	blob, err := json.Marshal(blobItems)
	if err != nil {
		h.logger.Printf("ERROR admin students marshal: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	page := AdminStudentsPageData{
		PageTitle:    "Ученики",
		Footer:       h.buildFooterInfo(),
		Students:     views,
		StudentsJSON: template.JS(blob),
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_students.html", page); err != nil {
		h.logger.Printf("ERROR render admin students: %v", err)
	}
}

type adminStudentSaveRequest struct {
	ID         string `json:"id"`
	FullName   string `json:"full_name"`
	PublicName string `json:"public_name"`
	Accounts   []struct {
		Site      string `json:"site"`
		AccountID string `json:"account_id"`
	} `json:"accounts"`
}

func (h *Handlers) AdminStudentSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req adminStudentSaveRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}

	fullName := domain.NormalizeWhitespace(req.FullName)
	if fullName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ФИО обязательно"})
		return
	}

	accounts := make([]domain.Account, 0, len(req.Accounts))
	for _, a := range req.Accounts {
		accounts = append(accounts, domain.Account{Site: a.Site, AccountID: a.AccountID})
	}
	accounts = domain.NormalizeAccounts(accounts)
	publicName := domain.NormalizeWhitespace(req.PublicName)

	students, err := h.loadStudentsList()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	id := domain.NormalizeID(req.ID)
	savedID := id
	updated := false
	if id != "" {
		for i := range students {
			if strings.TrimSpace(students[i].ID) == id {
				students[i].FullName = fullName
				if publicName != "" {
					students[i].PublicName = publicName
				} else if strings.TrimSpace(students[i].PublicName) == "" {
					students[i].PublicName = studentintake.GeneratePublicNameFromFullName(fullName)
				}
				students[i].Accounts = accounts
				updated = true
				break
			}
		}
	}
	if !updated {
		newID := studentintake.GenerateUniqueID(fullName, func(candidate string) bool {
			for _, s := range students {
				if strings.TrimSpace(s.ID) == candidate {
					return true
				}
			}
			return false
		})
		if publicName == "" {
			publicName = studentintake.GeneratePublicNameFromFullName(fullName)
		}
		students = append(students, domain.Student{ID: newID, FullName: fullName, PublicName: publicName, Accounts: accounts})
		savedID = newID
	}

	if err := studentintake.WriteStudentsFile(h.dataPath("students.json"), students); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": savedID})
}

func (h *Handlers) AdminStudentDelete(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	id := domain.NormalizeID(req.ID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id обязателен"})
		return
	}

	students, err := h.loadStudentsList()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	kept := make([]domain.Student, 0, len(students))
	for _, s := range students {
		if strings.TrimSpace(s.ID) == id {
			continue
		}
		kept = append(kept, s)
	}
	if err := studentintake.WriteStudentsFile(h.dataPath("students.json"), kept); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Убираем ученика из student_ids всех групп, чтобы не осталось «висячих» ссылок.
	h.removeStudentFromAllGroups(id)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handlers) removeStudentFromAllGroups(studentID string) {
	entries, err := os.ReadDir(h.dataPath("groups"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !domain.IsValidSlug(entry.Name()) {
			continue
		}
		groupFile, ok, err := h.readGroupFile(entry.Name())
		if err != nil || !ok {
			continue
		}
		filtered := make([]string, 0, len(groupFile.StudentIDs))
		changed := false
		for _, sid := range groupFile.StudentIDs {
			if strings.TrimSpace(sid) == studentID {
				changed = true
				continue
			}
			filtered = append(filtered, sid)
		}
		if !changed {
			continue
		}
		groupFile.StudentIDs = filtered
		if err := h.writeGroupFile(entry.Name(), groupFile); err != nil {
			h.logger.Printf("WARN remove student %s from group %s: %v", studentID, entry.Name(), err)
		}
	}
}

func (h *Handlers) writeGroupFile(slug string, groupFile domain.GroupFile) error {
	if groupFile.StudentIDs == nil {
		groupFile.StudentIDs = []string{}
	}
	return fileutil.WriteJSON(h.dataPath("groups", slug, "group.json"), groupFile, 0o644)
}

// ---- Управление группой (участники + контесты) ----

var groupInlineContestKeys = []string{
	"title", "score_system", "source_type", "contest_type", "provider", "provider_config", "subcontests", "materials",
}

type AdminGroupManagePageData struct {
	PageTitle   string
	Footer      FooterInfo
	GroupSlug   string
	GroupTitle  string
	Members     []AdminGroupMember
	Contests    []AdminGroupContestRow
	InlineCount int
	HasGrades   bool
}

type AdminGroupMember struct {
	StudentID  string
	PublicName string
}

type AdminGroupContestRow struct {
	ID        string
	Title     string
	Attached  bool
	Update    bool
	TableName string
	Missing   bool // ссылка на контест, которого нет в глобальном contests.json
}

// groupContestEntry — разобранный элемент groups/<slug>/contests.json.
type groupContestEntry struct {
	raw       json.RawMessage
	id        string
	inline    bool
	update    bool
	tableName string
}

func (h *Handlers) loadGroupContestEntries(slug string) ([]groupContestEntry, error) {
	var raw []json.RawMessage
	if err := fileutil.ReadJSON(h.dataPath("groups", slug, "contests.json"), &raw); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]groupContestEntry, 0, len(raw))
	for _, item := range raw {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(item, &keys); err != nil {
			continue
		}
		var meta struct {
			ID         string               `json:"id"`
			Update     *bool                `json:"update"`
			TableNames domain.TableNameList `json:"table_name"`
		}
		_ = json.Unmarshal(item, &meta)

		entry := groupContestEntry{
			raw:       item,
			id:        strings.TrimSpace(meta.ID),
			update:    true,
			tableName: strings.Join(meta.TableNames, ", "),
		}
		if meta.Update != nil {
			entry.update = *meta.Update
		}
		for _, key := range groupInlineContestKeys {
			if _, ok := keys[key]; ok {
				entry.inline = true
				break
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func (h *Handlers) AdminGroupManagePage(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if !domain.IsValidSlug(slug) {
		http.NotFound(w, r)
		return
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil {
		h.logger.Printf("ERROR admin group manage read group slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(groupFile.Title)
	if title == "" {
		title = slug
	}

	publicNames := h.loadPublicNames()
	members := make([]AdminGroupMember, 0, len(groupFile.StudentIDs))
	for _, sid := range domain.NormalizeGroups(groupFile.StudentIDs) {
		name := publicNames[sid]
		if name == "" {
			name = sid
		}
		members = append(members, AdminGroupMember{StudentID: sid, PublicName: name})
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		h.logger.Printf("ERROR admin group manage read contests slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	refByID := make(map[string]groupContestEntry)
	refOrder := make([]string, 0)
	inlineCount := 0
	for _, e := range entries {
		if e.inline {
			inlineCount++
			continue
		}
		if e.id == "" {
			continue
		}
		if _, dup := refByID[e.id]; !dup {
			refOrder = append(refOrder, e.id)
		}
		refByID[e.id] = e
	}

	globalContests, err := h.loadContestsList()
	if err != nil {
		h.logger.Printf("ERROR admin group manage read global contests: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	titleByID := make(map[string]string, len(globalContests))
	globalOrder := make([]string, 0, len(globalContests))
	for _, c := range globalContests {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		titleByID[id] = strings.TrimSpace(c.Title)
		globalOrder = append(globalOrder, id)
	}

	rows := make([]AdminGroupContestRow, 0)
	seen := make(map[string]struct{})
	appendRow := func(id string) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ref, attached := refByID[id]
		ctitle, inGlobal := titleByID[id]
		if ctitle == "" {
			ctitle = id
		}
		row := AdminGroupContestRow{ID: id, Title: ctitle, Attached: attached, Update: true, Missing: !inGlobal}
		if attached {
			row.Update = ref.update
			row.TableName = ref.tableName
		}
		rows = append(rows, row)
	}
	for _, id := range refOrder { // текущие ссылки группы — в их порядке
		appendRow(id)
	}
	for _, id := range globalOrder { // остальные глобальные контесты
		appendRow(id)
	}

	page := AdminGroupManagePageData{
		PageTitle:   "Группа: " + title,
		Footer:      h.buildFooterInfo(),
		GroupSlug:   slug,
		GroupTitle:  title,
		Members:     members,
		Contests:    rows,
		InlineCount: inlineCount,
		HasGrades:   groupFile.Grades != nil && len(groupFile.Grades.Columns) > 0,
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_group.html", page); err != nil {
		h.logger.Printf("ERROR render admin group manage slug=%s: %v", slug, err)
	}
}

func (h *Handlers) AdminGroupMemberRemove(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug      string `json:"slug"`
		StudentID string `json:"student_id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	studentID := strings.TrimSpace(req.StudentID)
	if !domain.IsValidSlug(slug) || studentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}
	filtered := make([]string, 0, len(groupFile.StudentIDs))
	for _, sid := range groupFile.StudentIDs {
		if strings.TrimSpace(sid) == studentID {
			continue
		}
		filtered = append(filtered, sid)
	}
	groupFile.StudentIDs = filtered
	if err := h.writeGroupFile(slug, groupFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handlers) AdminGroupContestsSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug     string `json:"slug"`
		Contests []struct {
			ID        string `json:"id"`
			Update    bool   `json:"update"`
			TableName string `json:"table_name"`
		} `json:"contests"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !domain.IsValidSlug(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid slug"})
		return
	}
	if _, ok, err := h.readGroupFile(slug); err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}

	// Сохраняем inline-контесты как есть, ссылки — из формы.
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]json.RawMessage, 0, len(req.Contests)+len(entries))
	seen := make(map[string]struct{})
	for _, c := range req.Contests {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		entry := map[string]any{"id": id, "update": c.Update}
		if tn := parseTableNameField(c.TableName); len(tn) == 1 {
			entry["table_name"] = tn[0]
		} else if len(tn) > 1 {
			entry["table_name"] = tn
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		out = append(out, encoded)
	}
	for _, e := range entries { // inline-контесты сохраняем без изменений
		if e.inline {
			out = append(out, e.raw)
		}
	}

	if err := fileutil.WriteJSON(h.dataPath("groups", slug, "contests.json"), out, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// parseTableNameField разбирает поле table_name из формы: пусто → nil,
// одиночное имя → [name], через запятую → несколько.
func parseTableNameField(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- Контесты ----

type AdminContestsPageData struct {
	PageTitle string
	Footer    FooterInfo
	Contests  []AdminContestView
}

type AdminContestView struct {
	ID          string
	Title       string
	Type        string
	ScoreSystem string
}

func (h *Handlers) AdminContestsPage(w http.ResponseWriter, _ *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	contests, err := h.loadContestsList()
	if err != nil {
		h.logger.Printf("ERROR admin contests load: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]AdminContestView, 0, len(contests))
	for _, c := range contests {
		views = append(views, AdminContestView{
			ID: c.ID, Title: c.Title, Type: c.TypeOrDefault(), ScoreSystem: string(c.ScoreSystem.Normalized()),
		})
	}

	blob, err := json.Marshal(contests)
	if err != nil {
		h.logger.Printf("ERROR admin contests marshal: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	page := struct {
		AdminContestsPageData
		ContestsJSON template.JS
	}{
		AdminContestsPageData: AdminContestsPageData{
			PageTitle: "Контесты",
			Footer:    h.buildFooterInfo(),
			Contests:  views,
		},
		ContestsJSON: template.JS(blob),
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_contests.html", page); err != nil {
		h.logger.Printf("ERROR render admin contests: %v", err)
	}
}

type adminContestSaveRequest struct {
	OriginalID  string `json:"original_id"`
	ID          string `json:"id"`
	Title       string `json:"title"`
	ScoreSystem string `json:"score_system"`
	SourceType  string `json:"source_type"`
	TableName   string `json:"table_name"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	Materials   []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"materials"`
	Subcontests []struct {
		Title string   `json:"title"`
		Tasks []string `json:"tasks"`
	} `json:"subcontests"`
	Provider       string `json:"provider"`
	ProviderConfig string `json:"provider_config"`
}

func (h *Handlers) AdminContestSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req adminContestSaveRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id обязателен"})
		return
	}

	contest := domain.Contest{
		ID:          id,
		Title:       strings.TrimSpace(req.Title),
		ScoreSystem: domain.ScoreSystem(req.ScoreSystem).Normalized(),
		TableNames:  domain.NormalizeTableNames(parseTableNameField(req.TableName)),
	}

	materials := make([]domain.ContestMaterial, 0, len(req.Materials))
	for _, m := range req.Materials {
		materials = append(materials, domain.ContestMaterial{Title: m.Title, URL: m.URL})
	}
	contest.Materials = domain.NormalizeContestMaterials(materials)

	if strings.EqualFold(strings.TrimSpace(req.SourceType), domain.ContestTypeProvider) {
		contest.ContestType = domain.ContestTypeProvider
		contest.Provider = strings.TrimSpace(req.Provider)
		if contest.Provider == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "для provider-контеста укажите provider"})
			return
		}
		cfg := strings.TrimSpace(req.ProviderConfig)
		if cfg != "" {
			if !json.Valid([]byte(cfg)) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "provider_config: невалидный JSON"})
				return
			}
			contest.ProviderConfig = json.RawMessage(cfg)
		}
		contest.Subcontests = []domain.Subcontest{}
	} else {
		subcontests := make([]domain.Subcontest, 0, len(req.Subcontests))
		for _, sub := range req.Subcontests {
			tasks := make([]string, 0, len(sub.Tasks))
			for _, task := range sub.Tasks {
				if t := strings.TrimSpace(task); t != "" {
					tasks = append(tasks, t)
				}
			}
			subcontests = append(subcontests, domain.Subcontest{Title: strings.TrimSpace(sub.Title), Tasks: tasks})
		}
		contest.Subcontests = subcontests

		if start, ok := parseAdminTime(req.StartTime); ok {
			contest.StartTime = start
		} else if strings.TrimSpace(req.StartTime) != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "start_time: ожидается ISO (напр. 2026-09-01T18:00:00+03:00)"})
			return
		}
		if end, ok := parseAdminTime(req.EndTime); ok {
			contest.EndTime = end
		} else if strings.TrimSpace(req.EndTime) != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "end_time: ожидается ISO"})
			return
		}
	}

	contests, err := h.loadContestsList()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	originalID := strings.TrimSpace(req.OriginalID)
	// Проверка уникальности id (если id меняется или это новый контест).
	for _, c := range contests {
		if strings.TrimSpace(c.ID) == id && strings.TrimSpace(c.ID) != originalID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "контест с таким id уже есть"})
			return
		}
	}

	replaced := false
	for i := range contests {
		if strings.TrimSpace(contests[i].ID) == originalID && originalID != "" {
			contests[i] = contest
			replaced = true
			break
		}
	}
	if !replaced {
		contests = append(contests, contest)
	}

	if err := h.saveContests(contests); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (h *Handlers) AdminContestDelete(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id обязателен"})
		return
	}
	contests, err := h.loadContestsList()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	kept := make([]domain.Contest, 0, len(contests))
	for _, c := range contests {
		if strings.TrimSpace(c.ID) == id {
			continue
		}
		kept = append(kept, c)
	}
	if err := h.saveContests(kept); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handlers) saveContests(contests []domain.Contest) error {
	return fileutil.WriteJSON(h.dataPath("contests.json"), contests, 0o644)
}

func parseAdminTime(raw string) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, true
	}
	return nil, false
}
