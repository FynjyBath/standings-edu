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
