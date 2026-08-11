package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/source"
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
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	ShortName  string
	// FormLink — ссылка на форму регистрации/обновления аккаунтов (form_link).
	FormLink        string
	Members         []AdminGroupMember
	Entries         []AdminGroupContestEntry
	AddableContests []AdminGroupContestOption
	// CanAddGlobal/CanEditInline — показывать ли «Добавить из глобальных» и
	// работу со своими (inline) контестами. В админке оба true, у доступа
	// группы — по правам contests.global / contests.inline.
	CanAddGlobal  bool
	CanEditInline bool
	InlineJSON    template.JS
	// MembersJSON — участники группы для формы кондуита (сетка «Заполнить в
	// форме»): [{id, name}], где name — полное ФИО (для матчинга по имени).
	MembersJSON template.JS
	HasGrades   bool
	// Accesses — блок «Доступы к группе» (общий редактор, см. access_admin.go).
	Accesses AccessEditorData
	// ShowTaskLinks — показывать ли ссылки на задачи ученикам (по умолчанию да).
	ShowTaskLinks bool
	// Archived — группа в архиве (update=false).
	Archived bool
}

type AdminGroupMember struct {
	StudentID  string
	PublicName string
	FullName   string
}

type AdminGroupContestEntry struct {
	ID        string
	Title     string
	Update    bool
	TableName string
	StartTime string // локальное переопределение окна (ISO); пусто — как у контеста
	EndTime   string
	Freeze    string // локальное переопределение заморозки; пусто — как у контеста
	// ZeroPenalty/SummaryTotal/Hidden — локальные переопределения; пусто — как у
	// контеста. SummaryTotal/Hidden: "1"/"0" для селекта.
	ZeroPenalty  string
	SummaryTotal string
	Hidden       string
	// Inh* — эффективные значения из определения контеста (для подсказок
	// «как у контеста (…)» в UI). У inline пустые: их определение и есть запись.
	InhTableName    string
	InhStart        string
	InhEnd          string
	InhFreeze       string
	InhZeroPenalty  string
	InhSummaryTotal string // "да"/"нет"
	InhHidden       string // "да"/"нет"
	Inline          bool
	Missing         bool // ссылка на контест, которого нет в глобальном contests.json
	// Kind — человекочитаемый вид («задачи», «Moodle», «кондуит»…); IsProvider —
	// окно/заморозка/штраф к контесту не применяются (заменяются прочерком).
	Kind       string
	IsProvider bool
}

type AdminGroupContestOption struct {
	ID    string
	Title string
	Kind  string // вид provider-контеста для подписи в списке; пусто — задачи
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
	freeze    string // поле "freeze" записи как есть; пусто — не задано
	// Локальные переопределения (nil — не заданы, наследуются).
	zeroPenalty      *int
	summaryTotalOnly *bool
	hidden           *bool
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
			ID               string               `json:"id"`
			Update           *bool                `json:"update"`
			TableNames       domain.TableNameList `json:"table_name"`
			StartTime        string               `json:"start_time"`
			EndTime          string               `json:"end_time"`
			Freeze           string               `json:"freeze"`
			ZeroPenalty      *int                 `json:"zero_penalty"`
			SummaryTotalOnly *bool                `json:"summary_total_only"`
			Hidden           *bool                `json:"hidden"`
		}
		_ = json.Unmarshal(item, &meta)

		entry := groupContestEntry{
			raw:              item,
			id:               strings.TrimSpace(meta.ID),
			update:           true,
			tableName:        strings.Join(meta.TableNames, ", "),
			startTime:        strings.TrimSpace(meta.StartTime),
			endTime:          strings.TrimSpace(meta.EndTime),
			freeze:           strings.TrimSpace(meta.Freeze),
			zeroPenalty:      meta.ZeroPenalty,
			summaryTotalOnly: meta.SummaryTotalOnly,
			hidden:           meta.Hidden,
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
	page, ok, err := h.buildGroupManageData(slug)
	if err != nil {
		h.logger.Printf("ERROR admin group manage slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_group.html", page); err != nil {
		h.logger.Printf("ERROR render admin group manage slug=%s: %v", slug, err)
	}
}

// buildGroupManageData собирает данные страницы управления группой: участники,
// записи контестов (со всеми переопределениями и наследуемыми значениями),
// список добавляемых глобальных контестов, JSON inline-контестов и участников.
// Используется и админкой, и панелью группы (роль «админ группы»).
// ok=false — группы нет.
func (h *Handlers) buildGroupManageData(slug string) (AdminGroupManagePageData, bool, error) {
	var empty AdminGroupManagePageData
	if !domain.IsValidSlug(slug) {
		return empty, false, nil
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil {
		return empty, false, err
	}
	if !ok {
		return empty, false, nil
	}
	title := strings.TrimSpace(groupFile.Title)
	if title == "" {
		title = slug
	}

	studentsByID := h.loadStudentsByID()
	members := make([]AdminGroupMember, 0, len(groupFile.StudentIDs))
	for _, sid := range domain.NormalizeGroups(groupFile.StudentIDs) {
		s := studentsByID[sid]
		name := strings.TrimSpace(s.PublicName)
		if name == "" {
			name = sid
		}
		members = append(members, AdminGroupMember{StudentID: sid, PublicName: name, FullName: strings.TrimSpace(s.FullName)})
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return empty, false, err
	}
	globalContests, err := h.loadContestsList()
	if err != nil {
		return empty, false, err
	}
	globalByID := make(map[string]domain.Contest, len(globalContests))
	for _, c := range globalContests {
		if id := strings.TrimSpace(c.ID); id != "" {
			globalByID[id] = c
		}
	}

	// Список контестов самой группы (ссылки + inline), в их порядке.
	rows := make([]AdminGroupContestEntry, 0, len(entries))
	inGroup := make(map[string]struct{}, len(entries))
	inlineByID := make(map[string]domain.Contest)
	groupTables := loadManualTablesFile(h.groupManualTablesPath(slug))
	for _, e := range entries {
		if e.id == "" {
			continue
		}
		inGroup[e.id] = struct{}{}
		row := AdminGroupContestEntry{ID: e.id, Update: e.update, TableName: e.tableName, StartTime: e.startTime, EndTime: e.endTime, Freeze: e.freeze, Inline: e.inline}
		if e.zeroPenalty != nil {
			row.ZeroPenalty = strconv.Itoa(*e.zeroPenalty)
		}
		if e.summaryTotalOnly != nil {
			row.SummaryTotal = "0"
			if *e.summaryTotalOnly {
				row.SummaryTotal = "1"
			}
		}
		if e.hidden != nil {
			row.Hidden = "0"
			if *e.hidden {
				row.Hidden = "1"
			}
		}
		if e.inline {
			var inlineContest domain.Contest
			if err := json.Unmarshal(e.raw, &inlineContest); err == nil {
				// Для формы редактирования: таблица кондуита из manual_tables.json
				// группы подставляется обратно в конфиг.
				if t, ok := groupTables[e.id]; ok && strings.TrimSpace(inlineContest.Provider) == source.ManualTableProviderID {
					if cfg, err := source.InjectManualTable(inlineContest.ProviderConfig, t); err == nil {
						inlineContest.ProviderConfig = cfg
					}
				}
				inlineByID[e.id] = inlineContest
				row.Title = strings.TrimSpace(inlineContest.Title)
				row.Kind, row.IsProvider = contestKindLabel(inlineContest)
			}
			if row.Title == "" {
				row.Title = e.id
			}
		} else {
			def, ok := globalByID[e.id]
			row.Missing = !ok
			row.Title = strings.TrimSpace(def.Title)
			if row.Title == "" {
				row.Title = e.id
			}
			if ok {
				row.Kind, row.IsProvider = contestKindLabel(def)
			}
			// Наследуемые значения из определения — для подсказок «как у контеста».
			row.InhTableName = strings.Join(def.TableNames, ", ")
			if def.StartTime != nil {
				row.InhStart = def.StartTime.Format(time.RFC3339)
			}
			if def.EndTime != nil {
				row.InhEnd = def.EndTime.Format(time.RFC3339)
			}
			row.InhFreeze = strings.TrimSpace(def.Freeze)
			if def.ZeroPenalty > 0 {
				row.InhZeroPenalty = strconv.Itoa(def.ZeroPenalty)
			}
			row.InhSummaryTotal = "нет"
			if def.SummaryTotalOnly {
				row.InhSummaryTotal = "да"
			}
			row.InhHidden = "показан"
			if def.Hidden {
				row.InhHidden = "скрыт"
			}
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
		opt := AdminGroupContestOption{ID: id, Title: t}
		if kind, isProvider := contestKindLabel(c); isProvider {
			opt.Kind = kind
		}
		addable = append(addable, opt)
	}

	inlineBlob, err := json.Marshal(inlineByID)
	if err != nil {
		return empty, false, err
	}

	// Участники для сетки кондуита: полное ФИО, если есть (лучше матчится).
	type memberRef struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	memberRefs := make([]memberRef, 0, len(members))
	for _, m := range members {
		name := m.FullName
		if name == "" {
			name = m.PublicName
		}
		memberRefs = append(memberRefs, memberRef{ID: m.StudentID, Name: name})
	}
	membersBlob, err := json.Marshal(memberRefs)
	if err != nil {
		return empty, false, err
	}

	page := AdminGroupManagePageData{
		PageTitle:       "Группа: " + title,
		Footer:          h.buildFooterInfo(),
		GroupSlug:       slug,
		GroupTitle:      title,
		ShortName:       strings.TrimSpace(groupFile.ShortName),
		FormLink:        strings.TrimSpace(groupFile.FormLink),
		Members:         members,
		Entries:         rows,
		AddableContests: addable,
		CanAddGlobal:    true,
		CanEditInline:   true,
		InlineJSON:      template.JS(inlineBlob),
		MembersJSON:     template.JS(membersBlob),
		HasGrades:       groupFile.Grades != nil && len(groupFile.Grades.Columns) > 0,
		ShowTaskLinks:   groupFile.TaskLinksShown(),
		Archived:        groupFile.Update != nil && !*groupFile.Update,
	}
	// Легаси-поля показываем как обычные доступы: первое же сохранение
	// перенесёт их в accesses (см. AdminGroupAccessesSave).
	page.Accesses = h.buildAccessEditor(false, slug, "/api/admin/group/accesses/save", groupFile.EffectiveAccesses())
	return page, true, nil
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

// AdminGroupSetArchived архивирует/разархивирует группу: архив — это update=false
// у группы. Архивная группа не пересобирается при генерации, её страница
// остаётся из последней генерации, и в активных списках она не мелькает.
func (h *Handlers) AdminGroupSetArchived(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug     string `json:"slug"`
		Archived bool   `json:"archived"`
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
	if req.Archived {
		v := false
		groupFile.Update = &v // update=false — в архиве
	} else {
		groupFile.Update = nil // по умолчанию активна (update=true)
	}
	if err := h.writeGroupFile(slug, groupFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "archived": req.Archived})
}

// AdminGroupSetShortName сохраняет короткое название группы (пусто — убрать).
func (h *Handlers) AdminGroupSetShortName(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug      string `json:"slug"`
		ShortName string `json:"short_name"`
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
	groupFile.ShortName = strings.TrimSpace(req.ShortName)
	if err := h.writeGroupFile(slug, groupFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "short_name": groupFile.ShortName})
}

// AdminGroupSetFormLink сохраняет ссылку на форму регистрации (form_link);
// пустая строка — убрать ссылку. Ссылка попадает в таблицы при генерации,
// поэтому на странице группы она обновится после ближайшего generate.
func (h *Handlers) AdminGroupSetFormLink(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug     string `json:"slug"`
		FormLink string `json:"form_link"`
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
	link, msg := normalizeFormLink(req.FormLink)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": msg})
		return
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}
	groupFile.FormLink = link
	if err := h.writeGroupFile(slug, groupFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "form_link": link})
}

// normalizeFormLink проверяет ссылку на форму: пусто — убрать, иначе только
// http(s) с хостом (иначе шаблон всё равно вырежет её как небезопасную, и
// преподаватель увидит битую ссылку вместо формы).
func normalizeFormLink(raw string) (string, string) {
	link := strings.TrimSpace(raw)
	if link == "" {
		return "", ""
	}
	parsed, err := url.Parse(link)
	if err != nil {
		return "", "не разобрать ссылку: " + err.Error()
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", "ссылка должна начинаться с http:// или https://"
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", "в ссылке нет адреса сайта"
	}
	return link, ""
}

// AdminGroupSetShowTaskLinks переключает флаг show_task_links группы (показывать
// ли ссылки на задачи ученикам). Действует сразу при отдаче, без перегенерации.
func (h *Handlers) AdminGroupSetShowTaskLinks(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug string `json:"slug"`
		Show bool   `json:"show"`
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
	// true — поле убираем (значение по умолчанию), false — ставим явно.
	if req.Show {
		groupFile.ShowTaskLinks = nil
	} else {
		v := false
		groupFile.ShowTaskLinks = &v
	}
	if err := h.writeGroupFile(slug, groupFile); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "show": groupFile.TaskLinksShown()})
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

	if status, msg := h.addGroupContestRef(slug, id); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// addGroupContestRef добавляет ссылку на глобальный контест В НАЧАЛО списка
// контестов группы. Возвращает (status, "") при успехе или (status, ошибка).
// Общий код админки и жюри-панели (по токену группы).
func (h *Handlers) addGroupContestRef(slug, id string) (int, string) {
	globals, err := h.loadContestsList()
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	inGlobal := false
	for _, c := range globals {
		if strings.TrimSpace(c.ID) == id {
			inGlobal = true
			break
		}
	}
	if !inGlobal {
		return http.StatusBadRequest, "нет такого глобального контеста"
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	encoded, err := json.Marshal(map[string]any{"id": id, "update": true})
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	// Новый контест — сверху списка (обычно он самый актуальный).
	out := make([]json.RawMessage, 0, len(entries)+1)
	out = append(out, encoded)
	for _, e := range entries {
		if e.id == id {
			return http.StatusBadRequest, "контест уже добавлен в группу"
		}
		out = append(out, e.raw)
	}

	if err := h.writeGroupContestRaw(slug, out); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return http.StatusOK, ""
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
	if status, msg := h.removeGroupContestEntry(strings.TrimSpace(req.Slug), strings.TrimSpace(req.ID)); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// removeGroupContestEntry убирает контест из группы (и таблицу inline-кондуита,
// если она была). Пустое сообщение — успех.
func (h *Handlers) removeGroupContestEntry(slug, id string) (int, string) {
	if !domain.IsValidSlug(slug) || id == "" {
		return http.StatusBadRequest, "bad request"
	}
	if _, ok, err := h.readGroupFile(slug); err != nil || !ok {
		return http.StatusBadRequest, "group not found"
	}
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
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
		return http.StatusBadRequest, "контест не найден в группе"
	}
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	// Таблица inline-кондуита (если была) больше не нужна.
	if err := removeManualTablesEntry(h.groupManualTablesPath(slug), id); err != nil {
		h.logger.Printf("WARN remove group manual table slug=%s id=%s: %v", slug, id, err)
	}
	return http.StatusOK, ""
}

// AdminGroupContestMove меняет контест местами с соседним (вверх/вниз),
// переставляя его в порядке контестов группы.
func (h *Handlers) AdminGroupContestMove(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug string `json:"slug"`
		ID   string `json:"id"`
		Dir  string `json:"dir"` // "up" | "down"
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	id := strings.TrimSpace(req.ID)
	dir := strings.TrimSpace(req.Dir)
	if !domain.IsValidSlug(slug) || id == "" || (dir != "up" && dir != "down") {
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
	if status, msg := h.moveGroupContestEntry(slug, id, dir, entries); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// moveGroupContestEntry меняет контест местами с соседним по уже загруженным
// записям группы. Общий код админки и жюри-панели.
func (h *Handlers) moveGroupContestEntry(slug, id, dir string, entries []groupContestEntry) (int, string) {
	idx := -1
	for i, e := range entries {
		if e.id == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return http.StatusBadRequest, "контест не найден в группе"
	}
	swap := idx - 1
	if dir == "down" {
		swap = idx + 1
	}
	if swap < 0 || swap >= len(entries) {
		return http.StatusBadRequest, "контест уже с краю"
	}

	out := make([]json.RawMessage, len(entries))
	for i, e := range entries {
		out[i] = e.raw
	}
	out[idx], out[swap] = out[swap], out[idx]
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return http.StatusOK, ""
}

// AdminGroupContestSetOptions меняет entry-настройки контеста группы: update,
// freeze, table_name и окно start/end — одинаково для ссылок и inline (у inline
// table_name и окно лежат в том же объекте, что и тело контеста).
func (h *Handlers) AdminGroupContestSetOptions(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req adminGroupContestOptionsRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	if status, msg := h.setGroupContestOptions(strings.TrimSpace(req.Slug), req); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminGroupContestOptionsRequest — настройки записи контеста в группе. Пустая
// строка / null у переопределяемых полей — «как у контеста».
type adminGroupContestOptionsRequest struct {
	Slug             string `json:"slug"`
	ID               string `json:"id"`
	Update           bool   `json:"update"`
	TableName        string `json:"table_name"`
	StartTime        string `json:"start_time"`
	EndTime          string `json:"end_time"`
	Freeze           string `json:"freeze"`
	ZeroPenalty      *int   `json:"zero_penalty"`
	SummaryTotalOnly *bool  `json:"summary_total_only"`
	Hidden           *bool  `json:"hidden"`
}

// setGroupContestOptions применяет настройки записи контеста группы (общий код
// админки и панели). Пустое сообщение — успех.
func (h *Handlers) setGroupContestOptions(slug string, req adminGroupContestOptionsRequest) (int, string) {
	id := strings.TrimSpace(req.ID)
	if !domain.IsValidSlug(slug) || id == "" {
		return http.StatusBadRequest, "bad request"
	}
	startTime, ok := parseAdminTime(req.StartTime)
	if !ok && strings.TrimSpace(req.StartTime) != "" {
		return http.StatusBadRequest, "start_time: ожидается ISO (напр. 2026-09-01T18:00:00+03:00)"
	}
	endTime, ok := parseAdminTime(req.EndTime)
	if !ok && strings.TrimSpace(req.EndTime) != "" {
		return http.StatusBadRequest, "end_time: ожидается ISO"
	}
	freeze := strings.TrimSpace(req.Freeze)
	if _, err := domain.ParseFreezeSpec(freeze); err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if req.ZeroPenalty != nil && *req.ZeroPenalty < 0 {
		return http.StatusBadRequest, "zero_penalty: ожидается неотрицательное число"
	}
	if _, ok, err := h.readGroupFile(slug); err != nil || !ok {
		return http.StatusBadRequest, "group not found"
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
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
				return http.StatusInternalServerError, err.Error()
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
			if req.ZeroPenalty != nil {
				blob, _ := json.Marshal(*req.ZeroPenalty)
				m["zero_penalty"] = blob
			} else {
				delete(m, "zero_penalty")
			}
			if req.SummaryTotalOnly != nil {
				blob, _ := json.Marshal(*req.SummaryTotalOnly)
				m["summary_total_only"] = blob
			} else {
				delete(m, "summary_total_only")
			}
			if req.Hidden != nil {
				blob, _ := json.Marshal(*req.Hidden)
				m["hidden"] = blob
			} else {
				delete(m, "hidden")
			}
			encoded, err := json.Marshal(m)
			if err != nil {
				return http.StatusInternalServerError, err.Error()
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
			if req.ZeroPenalty != nil {
				entry["zero_penalty"] = *req.ZeroPenalty
			}
			if req.SummaryTotalOnly != nil {
				entry["summary_total_only"] = *req.SummaryTotalOnly
			}
			if req.Hidden != nil {
				entry["hidden"] = *req.Hidden
			}
			encoded, err := json.Marshal(entry)
			if err != nil {
				return http.StatusInternalServerError, err.Error()
			}
			out = append(out, encoded)
		}
	}
	if !found {
		return http.StatusBadRequest, "контест не найден в группе"
	}
	if err := h.writeGroupContestRaw(slug, out); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return http.StatusOK, ""
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

	id, code, msg := h.saveInlineContest(slug, req.adminContestSaveRequest, req.Update)
	if msg != "" {
		writeJSON(w, code, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// saveInlineContest создаёт/обновляет inline-контест группы (общий код админа и
// жюри). updateOverride!=nil задаёт entry-поле update; иначе — прежнее значение
// при редактировании, true у нового. Возвращает (id, httpStatus, errMsg==""=ок).
func (h *Handlers) saveInlineContest(slug string, req adminContestSaveRequest, updateOverride *bool) (string, int, string) {
	contest, err := buildContestFromRequest(req, h.informaticsBaseURL)
	if err != nil {
		return "", http.StatusBadRequest, err.Error()
	}
	id := contest.ID
	originalID := strings.TrimSpace(req.OriginalID)

	// Кондуит: таблица оценок — в manual_tables.json группы, не в определении.
	if originalID != "" && originalID != id {
		if err := renameManualTablesEntry(h.groupManualTablesPath(slug), originalID, id); err != nil {
			return "", http.StatusInternalServerError, err.Error()
		}
	}
	if err := h.splitContestManualTable(&contest, h.groupManualTablesPath(slug)); err != nil {
		return "", http.StatusBadRequest, err.Error()
	}

	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return "", http.StatusInternalServerError, err.Error()
	}
	// id должен быть уникален среди контестов группы (кроме редактируемого).
	for _, e := range entries {
		if e.id == id && e.id != originalID {
			return "", http.StatusBadRequest, "контест с таким id уже есть в группе"
		}
	}

	// Entry-поле update форма контеста не редактирует: при редактировании
	// сохраняем прежнее значение записи, для нового контеста — true.
	update := true
	if originalID != "" {
		for _, e := range entries {
			if e.id == originalID {
				update = e.update
				break
			}
		}
	}
	if updateOverride != nil {
		update = *updateOverride
	}

	// Сериализуем контест и добавляем entry-level поле "update".
	contestBlob, err := json.Marshal(contest)
	if err != nil {
		return "", http.StatusInternalServerError, err.Error()
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(contestBlob, &m); err != nil {
		return "", http.StatusInternalServerError, err.Error()
	}
	updBlob, _ := json.Marshal(update)
	m["update"] = updBlob
	encoded, err := json.Marshal(m)
	if err != nil {
		return "", http.StatusInternalServerError, err.Error()
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
		return "", http.StatusInternalServerError, err.Error()
	}
	return id, http.StatusOK, ""
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

// contestKindLabel — человекочитаемый вид контеста для админских списков:
// «задачи» или конкретный провайдер («Moodle», «кондуит»…). Второе значение —
// это provider-контест (окно/заморозка/штраф к нему не применяются).
func contestKindLabel(c domain.Contest) (string, bool) {
	if c.TypeOrDefault() != domain.ContestTypeProvider {
		return "задачи", false
	}
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case "codeforces_contest":
		return "CF-контест", true
	case "html_table_import":
		return "HTML-импорт", true
	case "moodle_grades":
		return "Moodle", true
	case "manual_table":
		return "кондуит", true
	case "":
		return "provider", true
	default:
		return strings.TrimSpace(c.Provider), true
	}
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
		kind, _ := contestKindLabel(c)
		views = append(views, AdminContestView{
			ID: c.ID, Title: c.Title, Type: kind, ScoreSystem: string(c.ScoreSystem.Normalized()),
		})
	}

	// Для формы редактирования: подставляем таблицы кондуитов из
	// manual_tables.json обратно в provider_config (на диске они раздельно).
	globalTables := loadManualTablesFile(h.globalManualTablesPath())
	for i := range contests {
		if strings.TrimSpace(contests[i].Provider) != source.ManualTableProviderID {
			continue
		}
		if t, ok := globalTables[strings.TrimSpace(contests[i].ID)]; ok {
			if cfg, err := source.InjectManualTable(contests[i].ProviderConfig, t); err == nil {
				contests[i].ProviderConfig = cfg
			}
		}
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
	OriginalID       string                 `json:"original_id"`
	ID               string                 `json:"id"`
	Title            string                 `json:"title"`
	ShortName        string                 `json:"short_name"`
	ScoreSystem      string                 `json:"score_system"`
	SourceType       string                 `json:"source_type"`
	TableName        string                 `json:"table_name"`
	StartTime        string                 `json:"start_time"`
	EndTime          string                 `json:"end_time"`
	ZeroPenalty      int                    `json:"zero_penalty"`
	SummaryTotalOnly bool                   `json:"summary_total_only"`
	Hidden           bool                   `json:"hidden"`
	Freeze           string                 `json:"freeze"`
	Materials        []contestMaterialReq   `json:"materials"`
	Subcontests      []contestSubcontestReq `json:"subcontests"`
	Provider         string                 `json:"provider"`
	ProviderConfig   string                 `json:"provider_config"`
}

type contestMaterialReq struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type contestSubcontestReq struct {
	Title string   `json:"title"`
	Tasks []string `json:"tasks"`
}

// buildContestFromRequest собирает domain.Contest из данных формы (общий код для
// глобальных контестов и inline-контестов группы). Возвращает ошибку валидации.
// informaticsBase — зеркало informatics из кредов: ссылки на задачи, материалы и
// адреса внутри provider_config сразу приводятся к нему, чтобы в данных не
// смешивались msk и mccme. Пусто — ссылки сохраняются как введены.
func buildContestFromRequest(req adminContestSaveRequest, informaticsBase string) (domain.Contest, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return domain.Contest{}, errors.New("id обязателен")
	}

	if req.ZeroPenalty < 0 {
		return domain.Contest{}, errors.New("zero_penalty: ожидается неотрицательное число")
	}
	freeze := strings.TrimSpace(req.Freeze)
	if _, err := domain.ParseFreezeSpec(freeze); err != nil {
		return domain.Contest{}, err
	}
	contest := domain.Contest{
		ID:               id,
		Title:            strings.TrimSpace(req.Title),
		ShortName:        strings.TrimSpace(req.ShortName),
		ScoreSystem:      domain.ScoreSystem(req.ScoreSystem).Normalized(),
		TableNames:       domain.NormalizeTableNames(parseTableNameField(req.TableName)),
		ZeroPenalty:      req.ZeroPenalty,
		SummaryTotalOnly: req.SummaryTotalOnly,
		Hidden:           req.Hidden,
		Freeze:           freeze,
	}

	materials := make([]domain.ContestMaterial, 0, len(req.Materials))
	for _, m := range req.Materials {
		materials = append(materials, domain.ContestMaterial{
			Title: m.Title,
			URL:   domain.RewriteInformaticsHost(m.URL, informaticsBase),
		})
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
			contest.ProviderConfig = json.RawMessage(domain.RewriteInformaticsHostsInText(cfg, informaticsBase))
		}
		contest.Subcontests = []domain.Subcontest{}
		return contest, nil
	}

	subcontests := make([]domain.Subcontest, 0, len(req.Subcontests))
	for _, sub := range req.Subcontests {
		tasks := make([]string, 0, len(sub.Tasks))
		for _, task := range sub.Tasks {
			if t := strings.TrimSpace(task); t != "" {
				tasks = append(tasks, domain.RewriteInformaticsHost(t, informaticsBase))
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

	contest, err := buildContestFromRequest(req, h.informaticsBaseURL)
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

	// Кондуит: таблица оценок уезжает в manual_tables.json (при переименовании
	// запись переносится под новый id), в определении остаётся только конфиг.
	if originalID != "" && originalID != id {
		if err := renameManualTablesEntry(h.globalManualTablesPath(), originalID, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	if err := h.splitContestManualTable(&contest, h.globalManualTablesPath()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
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
	// Таблица кондуита (если была) больше не нужна.
	if err := removeManualTablesEntry(h.globalManualTablesPath(), id); err != nil {
		h.logger.Printf("WARN remove manual table for deleted contest %q: %v", id, err)
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
