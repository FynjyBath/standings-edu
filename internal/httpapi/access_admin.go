package httpapi

// Редактор доступов: один и тот же блок в админке группы и в глобальной
// админке. Разметку рисует JS по данным из JSON-полей (список доступов,
// каталог прав, пресеты, список групп), сохранение — списком целиком.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"standings-edu/internal/domain"
)

// AccessEditorData — данные блока «Доступы» для шаблона.
type AccessEditorData struct {
	// Global — редактируются глобальные доступы: доступна область действия и
	// право на каталог групп.
	Global bool
	// Slug — группа (для примеров ссылок); пусто у глобальных доступов.
	Slug string
	// SaveURL — куда постить список целиком.
	SaveURL string
	// AccessesJSON — текущий список доступов (с токенами и паролями: страница
	// админская, показывать их можно и нужно — иначе ссылку не отдать).
	AccessesJSON template.JS
	// CatalogJSON — разделы прав с названиями и подсказками.
	CatalogJSON template.JS
	// PresetsJSON — заготовки наборов прав.
	PresetsJSON template.JS
	// GroupsJSON — все группы (для области действия «выбранные»).
	GroupsJSON template.JS
}

// accessEditorGroup — группа в списке выбора области действия.
type accessEditorGroup struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// Archived — группа в архиве: в форме уезжает в свёрнутый блок, чтобы
	// старые группы не растягивали список на всю страницу.
	Archived bool `json:"archived"`
}

// buildAccessEditor собирает данные редактора. list — что показывать.
func (h *Handlers) buildAccessEditor(global bool, slug, saveURL string, list []domain.AccessEntry) AccessEditorData {
	if list == nil {
		list = []domain.AccessEntry{}
	}
	// Enabled в JSON пусто = включён; для формы разворачиваем в явный флаг.
	type editorEntry struct {
		domain.AccessEntry
		Enabled bool   `json:"enabled"`
		Preset  string `json:"preset"`
	}
	rows := make([]editorEntry, 0, len(list))
	for _, e := range list {
		rows = append(rows, editorEntry{AccessEntry: e, Enabled: e.IsEnabled(), Preset: domain.PresetIDFor(e.Perms)})
	}

	data := AccessEditorData{Global: global, Slug: slug, SaveURL: saveURL}
	data.AccessesJSON = template.JS(mustJSON(rows))
	data.CatalogJSON = template.JS(mustJSON(domain.PermCatalog()))
	data.PresetsJSON = template.JS(mustJSON(domain.Presets()))
	if global {
		data.GroupsJSON = template.JS(mustJSON(h.accessEditorGroups()))
	} else {
		data.GroupsJSON = template.JS("[]")
	}
	return data
}

// accessEditorGroups — все настроенные группы с названиями (по алфавиту).
func (h *Handlers) accessEditorGroups() []accessEditorGroup {
	slugs := h.allGroupSlugs()
	out := make([]accessEditorGroup, 0, len(slugs))
	for _, s := range slugs {
		item := accessEditorGroup{Slug: s, Title: s}
		if gf, ok := h.readSourceGroupFile(s); ok {
			if title := strings.TrimSpace(gf.Title); title != "" {
				item.Title = title
			}
			item.Archived = gf.Archived()
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Archived != out[j].Archived {
			return !out[i].Archived // активные выше архивных
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

// mustJSON — JSON для встраивания в шаблон (ошибка маршалинга невозможна на
// наших типах, но пустой массив всё равно безопаснее паники).
func mustJSON(v any) string {
	blob, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(blob)
}

// accessSaveRequest — тело сохранения списка доступов.
type accessSaveRequest struct {
	Slug     string               `json:"slug"`
	Accesses []domain.AccessEntry `json:"accesses"`
}

// normalizeAccesses проверяет и приводит список к записи: у новых записей
// появляется id, у токенных — сам токен, если его не сгенерировали в форме.
func normalizeAccesses(list []domain.AccessEntry, global bool) ([]domain.AccessEntry, error) {
	out := make([]domain.AccessEntry, 0, len(list))
	seenID := make(map[string]struct{}, len(list))
	seenLogin := make(map[string]struct{}, len(list))
	seenToken := make(map[string]struct{}, len(list))
	for i := range list {
		e := list[i]
		if e.Auth == domain.AccessAuthToken && strings.TrimSpace(e.Token) == "" {
			e.Token = newAccessToken()
		}
		if err := e.Validate(global); err != nil {
			return nil, err
		}
		e.ID = strings.TrimSpace(e.ID)
		if e.ID == "" {
			e.ID = newAccessID()
		}
		if _, dup := seenID[e.ID]; dup {
			e.ID = newAccessID()
		}
		seenID[e.ID] = struct{}{}

		// Совпадающие логины или токены — почти наверняка опечатка, а по факту
		// один доступ перекрывал бы другой.
		if e.UsesPassword() {
			key := strings.ToLower(e.Login)
			if _, dup := seenLogin[key]; dup {
				return nil, fmt.Errorf("логин %q встречается дважды", e.Login)
			}
			seenLogin[key] = struct{}{}
		}
		if e.UsesToken() {
			if _, dup := seenToken[e.Token]; dup {
				return nil, fmt.Errorf("доступ %q: такой токен уже используется", e.Title)
			}
			seenToken[e.Token] = struct{}{}
		}
		out = append(out, e)
	}
	return out, nil
}

// newAccessToken — секрет ссылки (32 hex-символа, как прежние токены групп).
func newAccessToken() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	return hex.EncodeToString(raw)
}

// newAccessID — идентификатор записи (внутренний, в ссылки не попадает).
func newAccessID() string {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "acc"
	}
	return "a" + hex.EncodeToString(raw)
}

// AdminGroupAccessesSave — сохранить список доступов группы (из админки).
func (h *Handlers) AdminGroupAccessesSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req accessSaveRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !domain.IsValidSlug(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid group slug"})
		return
	}
	list, err := normalizeAccesses(req.Accesses, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "группа не найдена"})
		return
	}
	// Первое сохранение вытесняет легаси: старые поля больше не читаются, и
	// оставлять их — значит хранить действующие пароли «про запас».
	gf.Accesses = list
	gf.PanelAccess = nil
	gf.GroupSecretToken = ""
	if err := h.writeGroupFile(slug, gf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	h.auditAdmin(r, "accesses.save", slug, fmt.Sprintf("доступов %d", len(list)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accesses": list})
}

// AdminGlobalAccessesSave — сохранить глобальные доступы.
func (h *Handlers) AdminGlobalAccessesSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req accessSaveRequest
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	list, err := normalizeAccesses(req.Accesses, true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := h.saveGlobalAccesses(list); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	h.auditAdmin(r, "global-accesses.save", "", fmt.Sprintf("доступов %d", len(list)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accesses": list})
}
