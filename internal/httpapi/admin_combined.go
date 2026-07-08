package httpapi

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"standings-edu/internal/domain"
)

// AdminCombinedGroup — объединённая группа для списка в админке.
type AdminCombinedGroup struct {
	Slug    string
	Title   string
	Members []AdminCombinedMember
	Token   string // group_secret_token (для ссылки жюри), пусто — токена нет
}

type AdminCombinedMember struct {
	Slug    string
	Title   string
	Missing bool // группа-участница удалена/не найдена
}

// AdminCombinedContest — контест в настройках объединённой группы: показывать/скрыть.
type AdminCombinedContest struct {
	ID     string
	Title  string
	Hidden bool
}

type AdminCombinedManagePageData struct {
	PageTitle    string
	Footer       FooterInfo
	Slug         string
	Title        string
	Token        string
	Members      []AdminCombinedMember
	Contests     []AdminCombinedContest
	NotGenerated bool // участницы ещё не сгенерированы — контестов нет
}

// AdminCombinedManagePage — страница настроек объединённой группы: токен жюри и
// список контестов с галочками «показывать в объединении».
func (h *Handlers) AdminCombinedManagePage(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if !domain.IsValidSlug(slug) {
		http.NotFound(w, r)
		return
	}
	gf, ok, err := h.readGroupFile(slug)
	if err != nil {
		h.logger.Printf("ERROR combined manage read group slug=%s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok || len(gf.MemberGroups) == 0 {
		http.NotFound(w, r)
		return
	}

	title := strings.TrimSpace(gf.Title)
	if title == "" {
		title = slug
	}

	// Названия групп-участниц.
	members := make([]AdminCombinedMember, 0, len(gf.MemberGroups))
	for _, member := range gf.MemberGroups {
		item := AdminCombinedMember{Slug: member, Title: member, Missing: true}
		if mf, ok := h.readSourceGroupFile(member); ok {
			item.Missing = false
			if t := strings.TrimSpace(mf.Title); t != "" {
				item.Title = t
			}
		}
		members = append(members, item)
	}

	// Полный список контестов объединения (без фильтра скрытых) + их состояние.
	hidden := make(map[string]struct{}, len(gf.HiddenContests))
	for _, id := range gf.HiddenContests {
		hidden[strings.TrimSpace(id)] = struct{}{}
	}
	merged := h.mergeCombinedMembers(slug, gf, nil)
	contests := make([]AdminCombinedContest, 0, len(merged.Contests))
	for _, c := range merged.Contests {
		_, isHidden := hidden[c.ID]
		contests = append(contests, AdminCombinedContest{ID: c.ID, Title: c.Title, Hidden: isHidden})
	}

	page := AdminCombinedManagePageData{
		PageTitle:    "Объединённая: " + title,
		Footer:       h.buildFooterInfo(),
		Slug:         slug,
		Title:        title,
		Token:        strings.TrimSpace(gf.GroupSecretToken),
		Members:      members,
		Contests:     contests,
		NotGenerated: len(contests) == 0,
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_combined.html", page); err != nil {
		h.logger.Printf("ERROR render combined manage slug=%s: %v", slug, err)
	}
}

// AdminCombinedSetContestHidden скрывает/показывает контест в объединённой группе.
func (h *Handlers) AdminCombinedSetContestHidden(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug      string `json:"slug"`
		ContestID string `json:"contest_id"`
		Hidden    bool   `json:"hidden"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	contestID := strings.TrimSpace(req.ContestID)
	if !domain.IsValidSlug(slug) || contestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok || len(gf.MemberGroups) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "это не объединённая группа"})
		return
	}

	// Пересобираем список скрытых: убираем этот id, при hidden=true добавляем.
	out := make([]string, 0, len(gf.HiddenContests)+1)
	for _, id := range gf.HiddenContests {
		id = strings.TrimSpace(id)
		if id != "" && id != contestID {
			out = append(out, id)
		}
	}
	if req.Hidden {
		out = append(out, contestID)
	}
	gf.HiddenContests = out
	if err := h.writeGroupFile(slug, gf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AdminCombinedGroupSave создаёт или обновляет объединённую группу: пишет
// data/groups/<slug>/group.json с member_groups. Обычную группу не перезаписывает.
func (h *Handlers) AdminCombinedGroupSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug         string   `json:"slug"`
		Title        string   `json:"title"`
		MemberGroups []string `json:"member_groups"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}

	slug := strings.TrimSpace(req.Slug)
	if !domain.IsValidSlug(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "slug: только латиница, цифры и дефис"})
		return
	}

	existing, exists, err := h.readGroupFile(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if exists && len(existing.MemberGroups) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "группа «" + slug + "» уже существует как обычная — выберите другой slug"})
		return
	}

	members := make([]string, 0, len(req.MemberGroups))
	seen := make(map[string]struct{})
	for _, member := range req.MemberGroups {
		member = strings.TrimSpace(member)
		if member == "" || member == slug || !domain.IsValidSlug(member) {
			continue
		}
		if _, dup := seen[member]; dup {
			continue
		}
		if _, ok, _ := h.readGroupFile(member); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "группа-участница не найдена: " + member})
			return
		}
		seen[member] = struct{}{}
		members = append(members, member)
	}
	if len(members) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "выберите хотя бы две группы"})
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = slug
	}

	gf := existing
	gf.Title = title
	gf.MemberGroups = members
	gf.StudentIDs = nil
	if err := h.writeGroupFile(slug, gf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": slug})
}

// AdminCombinedGroupDelete удаляет объединённую группу (её каталог). Обычные
// группы этим эндпоинтом не удаляются — только те, у кого задан member_groups.
func (h *Handlers) AdminCombinedGroupDelete(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug string `json:"slug"`
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
	gf, ok, err := h.readGroupFile(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !ok || len(gf.MemberGroups) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "это не объединённая группа"})
		return
	}
	if err := os.RemoveAll(h.dataPath("groups", slug)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// listCombinedGroups возвращает объединённые группы (с раскрытыми названиями
// участниц) и список обычных групп, которые можно выбрать в участницы.
func (h *Handlers) listCombinedGroups() (combined []AdminCombinedGroup, selectable []AdminGroupLink) {
	links, err := h.listAdminGroupLinks()
	if err != nil {
		return nil, nil
	}
	titleBySlug := make(map[string]string, len(links))
	for _, link := range links {
		titleBySlug[link.Slug] = link.Title
	}

	for _, link := range links {
		if !link.IsCombined {
			selectable = append(selectable, link)
			continue
		}
		item := AdminCombinedGroup{Slug: link.Slug, Title: link.Title}
		if gf, ok, _ := h.readGroupFile(link.Slug); ok {
			item.Token = strings.TrimSpace(gf.GroupSecretToken)
		}
		for _, member := range link.Members {
			title, ok := titleBySlug[member]
			item.Members = append(item.Members, AdminCombinedMember{Slug: member, Title: title, Missing: !ok})
		}
		combined = append(combined, item)
	}

	sort.Slice(combined, func(i, j int) bool { return combined[i].Title < combined[j].Title })
	sort.Slice(selectable, func(i, j int) bool { return selectable[i].Title < selectable[j].Title })
	return combined, selectable
}
