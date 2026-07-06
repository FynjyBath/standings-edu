package httpapi

import (
	"crypto/rand"
	"encoding/hex"
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

// groupInlineContestKeys — держать в синхроне с inlineContestKeys в
// internal/storage/source_loader.go: и админка, и генератор должны одинаково
// отличать inline-определение от ссылки по id.
var groupInlineContestKeys = []string{
	"title", "score_system", "source_type", "contest_type", "provider", "provider_config", "subcontests", "materials",
}

type AdminGroupManagePageData struct {
	PageTitle       string
	Footer          FooterInfo
	GroupSlug       string
	GroupTitle      string
	Members         []AdminGroupMember
	Entries         []AdminGroupContestEntry
	AddableContests []AdminGroupContestOption
	InlineJSON      template.JS
	HasGrades       bool
	SecretToken     string // group_secret_token — просмотр размороженных таблиц
}

type AdminGroupMember struct {
	StudentID  string
	PublicName string
}

type AdminGroupContestEntry struct {
	ID        string
	Title     string
	Update    bool
	TableName string
	StartTime string // окно контеста на стороне группы (ISO); пусто — не задано
	EndTime   string
	Freeze    string // заморозка: "all" или длительность от конца ("1h"); пусто — нет
	Inline    bool
	Missing   bool // ссылка на контест, которого нет в глобальном contests.json
}

type AdminGroupContestOption struct {
	ID    string
	Title string
}

// groupContestEntry — разобранный элемент groups/<slug>/contests.json.
type groupContestEntry struct {
	raw       json.RawMessage
	id        string
	inline    bool
	update    bool
	tableName string
	startTime string // ISO-представление окна из записи; пусто — не задано
	endTime   string
	freeze    string // поле "freeze" записи как есть; пусто — нет заморозки
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
			// Не-объект (битая запись): в UI не показываем, но raw сохраняем,
			// чтобы write-эндпоинты не удаляли его молча.
			out = append(out, groupContestEntry{raw: item, update: true})
			continue
		}
		var meta struct {
			ID         string               `json:"id"`
			Update     *bool                `json:"update"`
			TableNames domain.TableNameList `json:"table_name"`
			StartTime  string               `json:"start_time"`
			EndTime    string               `json:"end_time"`
			Freeze     string               `json:"freeze"`
		}
		_ = json.Unmarshal(item, &meta)

		entry := groupContestEntry{
			raw:       item,
			id:        strings.TrimSpace(meta.ID),
			update:    true,
			tableName: strings.Join(meta.TableNames, ", "),
			startTime: strings.TrimSpace(meta.StartTime),
			endTime:   strings.TrimSpace(meta.EndTime),
			freeze:    strings.TrimSpace(meta.Freeze),
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
	globalContests, err := h.loadContestsList()
	if err != nil {
		h.logger.Printf("ERROR admin group manage read global contests: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	titleByID := make(map[string]string, len(globalContests))
	for _, c := range globalContests {
		if id := strings.TrimSpace(c.ID); id != "" {
			titleByID[id] = strings.TrimSpace(c.Title)
		}
	}

	// Список контестов самой группы (ссылки + inline), в их порядке.
	rows := make([]AdminGroupContestEntry, 0, len(entries))
	inGroup := make(map[string]struct{}, len(entries))
	inlineByID := make(map[string]domain.Contest)
	for _, e := range entries {
		if e.id == "" {
			continue
		}
		inGroup[e.id] = struct{}{}
		row := AdminGroupContestEntry{ID: e.id, Update: e.update, TableName: e.tableName, StartTime: e.startTime, EndTime: e.endTime, Freeze: e.freeze, Inline: e.inline}
		if e.inline {
			var inlineContest domain.Contest
			if err := json.Unmarshal(e.raw, &inlineContest); err == nil {
				inlineByID[e.id] = inlineContest
				row.Title = strings.TrimSpace(inlineContest.Title)
			}
			if row.Title == "" {
				row.Title = e.id
			}
		} else {
			ctitle, ok := titleByID[e.id]
			row.Missing = !ok
			if ctitle == "" {
				ctitle = e.id
			}
			row.Title = ctitle
		}
		rows = append(rows, row)
	}

	// Глобальные контесты, которых ещё нет в группе — для выпадающего «добавить ссылку».
	addable := make([]AdminGroupContestOption, 0)
	for _, c := range globalContests {
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
		addable = append(addable, AdminGroupContestOption{ID: id, Title: t})
	}

	inlineBlob, err := json.Marshal(inlineByID)
	if err != nil {
		h.logger.Printf("ERROR admin group manage marshal inline: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	page := AdminGroupManagePageData{
		PageTitle:       "Группа: " + title,
		Footer:          h.buildFooterInfo(),
		GroupSlug:       slug,
		GroupTitle:      title,
		Members:         members,
		Entries:         rows,
		AddableContests: addable,
		InlineJSON:      template.JS(inlineBlob),
		HasGrades:       groupFile.Grades != nil && len(groupFile.Grades.Columns) > 0,
		SecretToken:     strings.TrimSpace(groupFile.GroupSecretToken),
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

// AdminGroupTokenSet генерирует или удаляет group_secret_token группы —
// секрет для просмотра размороженных таблиц (?token=… на страницах группы).
// Действует сразу, без регенерации: проверка идёт при отдаче.
func (h *Handlers) AdminGroupTokenSet(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug  string `json:"slug"`
		Clear bool   `json:"clear"`
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
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}

	token := ""
	if !req.Clear {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		token = hex.EncodeToString(raw)
	}
	groupFile.GroupSecretToken = token
	if err := h.writeGroupFile(slug, groupFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": token})
}

// writeGroupContestRaw атомарно перезаписывает groups/<slug>/contests.json
// набором сырых элементов (ссылки и inline вперемешку, порядок сохраняется).
func (h *Handlers) writeGroupContestRaw(slug string, entries []json.RawMessage) error {
	return fileutil.WriteJSON(h.dataPath("groups", slug, "contests.json"), entries, 0o644)
}

// AdminGroupContestAddRef добавляет в группу ссылку на глобальный контест.
func (h *Handlers) AdminGroupContestAddRef(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug string `json:"slug"`
		ID   string `json:"id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	id := strings.TrimSpace(req.ID)
	if !domain.IsValidSlug(slug) || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	if _, ok, err := h.readGroupFile(slug); err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}

	globals, err := h.loadContestsList()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	inGlobal := false
	for _, c := range globals {
		if strings.TrimSpace(c.ID) == id {
			inGlobal = true
			break
		}
	}
	if !inGlobal {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "нет такого глобального контеста"})
		return
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	encoded, err := json.Marshal(map[string]any{"id": id, "update": true})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Новый контест — сверху списка (обычно он самый актуальный).
	out := make([]json.RawMessage, 0, len(entries)+1)
	out = append(out, encoded)
	for _, e := range entries {
		if e.id == id {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "контест уже добавлен в группу"})
			return
		}
		out = append(out, e.raw)
	}

	if err := h.writeGroupContestRaw(slug, out); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AdminGroupContestRemove убирает контест группы (ссылку или inline) по id.
func (h *Handlers) AdminGroupContestRemove(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug string `json:"slug"`
		ID   string `json:"id"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	id := strings.TrimSpace(req.ID)
	if !domain.IsValidSlug(slug) || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	if _, ok, err := h.readGroupFile(slug); err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]json.RawMessage, 0, len(entries))
	removed := false
	for _, e := range entries {
		if e.id == id {
			removed = true
			continue
		}
		out = append(out, e.raw)
	}
	if !removed {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "контест не найден в группе"})
		return
	}
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AdminGroupContestSetOptions меняет entry-настройки контеста группы: update,
// freeze, table_name и окно start/end — одинаково для ссылок и inline (у inline
// table_name и окно лежат в том же объекте, что и тело контеста).
func (h *Handlers) AdminGroupContestSetOptions(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug      string `json:"slug"`
		ID        string `json:"id"`
		Update    bool   `json:"update"`
		TableName string `json:"table_name"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		Freeze    string `json:"freeze"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	id := strings.TrimSpace(req.ID)
	if !domain.IsValidSlug(slug) || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	startTime, ok := parseAdminTime(req.StartTime)
	if !ok && strings.TrimSpace(req.StartTime) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "start_time: ожидается ISO (напр. 2026-09-01T18:00:00+03:00)"})
		return
	}
	endTime, ok := parseAdminTime(req.EndTime)
	if !ok && strings.TrimSpace(req.EndTime) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "end_time: ожидается ISO"})
		return
	}
	freeze := strings.TrimSpace(req.Freeze)
	if _, err := domain.ParseFreezeSpec(freeze); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, ok, err := h.readGroupFile(slug); err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]json.RawMessage, 0, len(entries))
	found := false
	for _, e := range entries {
		if e.id != id {
			out = append(out, e.raw)
			continue
		}
		found = true
		if e.inline {
			// Правим только entry-поля ("update", "freeze") внутри существующего
			// объекта, тело контеста — как есть.
			var m map[string]json.RawMessage
			if err := json.Unmarshal(e.raw, &m); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			upd, _ := json.Marshal(req.Update)
			m["update"] = upd
			if freeze != "" {
				fz, _ := json.Marshal(freeze)
				m["freeze"] = fz
			} else {
				delete(m, "freeze")
			}
			// table_name и окно у inline лежат в том же объекте — правим их
			// теми же полями формы, что и у ссылок (пусто — убрать).
			if tn := parseTableNameField(req.TableName); len(tn) == 1 {
				blob, _ := json.Marshal(tn[0])
				m["table_name"] = blob
			} else if len(tn) > 1 {
				blob, _ := json.Marshal(tn)
				m["table_name"] = blob
			} else {
				delete(m, "table_name")
			}
			if startTime != nil {
				blob, _ := json.Marshal(startTime)
				m["start_time"] = blob
			} else {
				delete(m, "start_time")
			}
			if endTime != nil {
				blob, _ := json.Marshal(endTime)
				m["end_time"] = blob
			} else {
				delete(m, "end_time")
			}
			encoded, err := json.Marshal(m)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			out = append(out, encoded)
		} else {
			entry := map[string]any{"id": id, "update": req.Update}
			if tn := parseTableNameField(req.TableName); len(tn) == 1 {
				entry["table_name"] = tn[0]
			} else if len(tn) > 1 {
				entry["table_name"] = tn
			}
			if startTime != nil {
				entry["start_time"] = startTime
			}
			if endTime != nil {
				entry["end_time"] = endTime
			}
			if freeze != "" {
				entry["freeze"] = freeze
			}
			encoded, err := json.Marshal(entry)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			out = append(out, encoded)
		}
	}
	if !found {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "контест не найден в группе"})
		return
	}
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AdminGroupContestInlineSave создаёт/редактирует inline-контест группы через
// ту же форму, что и глобальные контесты, но хранит его прямо в contests.json группы.
func (h *Handlers) AdminGroupContestInlineSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		adminContestSaveRequest
		Slug   string `json:"slug"`
		Update *bool  `json:"update"`
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

	contest, err := buildContestFromRequest(req.adminContestSaveRequest)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	id := contest.ID
	originalID := strings.TrimSpace(req.OriginalID)

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// id должен быть уникален среди контестов группы (кроме редактируемого).
	for _, e := range entries {
		if e.id == id && e.id != originalID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "контест с таким id уже есть в группе"})
			return
		}
	}

	// Entry-поля update/freeze форма контеста не редактирует: при редактировании
	// сохраняем прежние значения записи, для нового контеста — update=true без
	// заморозки. Явный update из запроса имеет приоритет.
	update := true
	freeze := ""
	if originalID != "" {
		for _, e := range entries {
			if e.id == originalID {
				update = e.update
				freeze = e.freeze
				break
			}
		}
	}
	if req.Update != nil {
		update = *req.Update
	}

	// Сериализуем контест и добавляем entry-level поле "update".
	contestBlob, err := json.Marshal(contest)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(contestBlob, &m); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	updBlob, _ := json.Marshal(update)
	m["update"] = updBlob
	if freeze != "" {
		fzBlob, _ := json.Marshal(freeze)
		m["freeze"] = fzBlob
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	out := make([]json.RawMessage, 0, len(entries)+1)
	replaced := false
	for _, e := range entries {
		if originalID != "" && e.id == originalID {
			out = append(out, encoded)
			replaced = true
			continue
		}
		out = append(out, e.raw)
	}
	if !replaced {
		// Новый inline-контест — сверху списка, как и добавление ссылки.
		out = append([]json.RawMessage{encoded}, out...)
	}

	if err := h.writeGroupContestRaw(slug, out); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
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
	OriginalID       string `json:"original_id"`
	ID               string `json:"id"`
	Title            string `json:"title"`
	ShortName        string `json:"short_name"`
	ScoreSystem      string `json:"score_system"`
	SourceType       string `json:"source_type"`
	TableName        string `json:"table_name"`
	StartTime        string `json:"start_time"`
	EndTime          string `json:"end_time"`
	ZeroPenalty      int    `json:"zero_penalty"`
	SummaryTotalOnly bool   `json:"summary_total_only"`
	Materials        []struct {
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

// buildContestFromRequest собирает domain.Contest из данных формы (общий код для
// глобальных контестов и inline-контестов группы). Возвращает ошибку валидации.
func buildContestFromRequest(req adminContestSaveRequest) (domain.Contest, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return domain.Contest{}, errors.New("id обязателен")
	}

	if req.ZeroPenalty < 0 {
		return domain.Contest{}, errors.New("zero_penalty: ожидается неотрицательное число")
	}
	contest := domain.Contest{
		ID:               id,
		Title:            strings.TrimSpace(req.Title),
		ShortName:        strings.TrimSpace(req.ShortName),
		ScoreSystem:      domain.ScoreSystem(req.ScoreSystem).Normalized(),
		TableNames:       domain.NormalizeTableNames(parseTableNameField(req.TableName)),
		ZeroPenalty:      req.ZeroPenalty,
		SummaryTotalOnly: req.SummaryTotalOnly,
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
			return domain.Contest{}, errors.New("для provider-контеста укажите provider")
		}
		cfg := strings.TrimSpace(req.ProviderConfig)
		if cfg != "" {
			if !json.Valid([]byte(cfg)) {
				return domain.Contest{}, errors.New("provider_config: невалидный JSON")
			}
			contest.ProviderConfig = json.RawMessage(cfg)
		}
		contest.Subcontests = []domain.Subcontest{}
		return contest, nil
	}

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
		return domain.Contest{}, errors.New("start_time: ожидается ISO (напр. 2026-09-01T18:00:00+03:00)")
	}
	if end, ok := parseAdminTime(req.EndTime); ok {
		contest.EndTime = end
	} else if strings.TrimSpace(req.EndTime) != "" {
		return domain.Contest{}, errors.New("end_time: ожидается ISO")
	}
	return contest, nil
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

	contest, err := buildContestFromRequest(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	id := contest.ID

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
