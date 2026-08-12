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
	"os"
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
	data := AccessEditorData{Global: global, Slug: slug, SaveURL: saveURL}
	data.AccessesJSON = template.JS(mustJSON(accessFormEntries(list)))
	data.CatalogJSON = template.JS(mustJSON(domain.PermCatalog()))
	data.PresetsJSON = template.JS(mustJSON(domain.Presets()))
	if global {
		data.GroupsJSON = template.JS(mustJSON(h.accessEditorGroups()))
	} else {
		data.GroupsJSON = template.JS("[]")
	}
	return data
}

// accessFormEntry — запись доступа для формы. Enabled в JSON пусто = включён,
// поэтому разворачиваем в явный флаг. Пароль наружу не уходит: на диске лежит
// хеш, показать его нечем и незачем — форме достаточно знать, что он задан.
type accessFormEntry struct {
	domain.AccessEntry
	Enabled     bool   `json:"enabled"`
	Preset      string `json:"preset"`
	HasPassword bool   `json:"has_password"`
}

// accessFormEntries — список доступов в виде, пригодном для показа в форме.
func accessFormEntries(list []domain.AccessEntry) []accessFormEntry {
	if list == nil {
		list = []domain.AccessEntry{}
	}
	rows := make([]accessFormEntry, 0, len(list))
	for _, e := range list {
		row := accessFormEntry{AccessEntry: e, Enabled: e.IsEnabled(), Preset: domain.PresetIDFor(e.Perms)}
		row.HasPassword = strings.TrimSpace(e.Password) != ""
		row.Password = ""
		rows = append(rows, row)
	}
	return rows
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
// появляется id, у токенных — сам токен, если его не сгенерировали в форме,
// а пароли уходят на диск только хешем. Пустое поле пароля означает «оставить
// прежний» — старый хеш берётся из stored по id записи.
func normalizeAccesses(list []domain.AccessEntry, stored []domain.AccessEntry, global bool) ([]domain.AccessEntry, error) {
	prevPassword := make(map[string]string, len(stored))
	for _, e := range stored {
		if id := strings.TrimSpace(e.ID); id != "" && strings.TrimSpace(e.Password) != "" {
			prevPassword[id] = e.Password
		}
	}

	out := make([]domain.AccessEntry, 0, len(list))
	seenID := make(map[string]struct{}, len(list))
	seenLogin := make(map[string]struct{}, len(list))
	seenToken := make(map[string]struct{}, len(list))
	for i := range list {
		e := list[i]
		if e.Auth == domain.AccessAuthToken && strings.TrimSpace(e.Token) == "" {
			e.Token = newAccessToken()
		}
		if e.Auth == domain.AccessAuthPassword && e.Password == "" {
			e.Password = prevPassword[strings.TrimSpace(e.ID)] // пустое поле — прежний пароль
		}
		if err := e.Validate(global); err != nil {
			return nil, err
		}
		// На диск пароль уходит только хешем (уже захешированный не трогаем —
		// иначе «оставить прежний» пересчитывало бы хеш от хеша).
		if e.Auth == domain.AccessAuthPassword && !domain.IsHashedPassword(e.Password) {
			hashed, err := domain.HashPassword(e.Password)
			if err != nil {
				return nil, fmt.Errorf("доступ %q: %w", e.Title, err)
			}
			e.Password = hashed
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
	gf, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "группа не найдена"})
		return
	}
	// Прежние пароли берём из того же списка, что показывали в форме: у легаси
	// это виртуальные записи из panel_access — сохранение их и переносит.
	list, err := normalizeAccesses(req.Accesses, gf.EffectiveAccesses(), false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accesses": accessFormEntries(list)})
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
	list, err := normalizeAccesses(req.Accesses, h.loadGlobalAccesses(), true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := h.saveGlobalAccesses(list); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	h.auditAdmin(r, "global-accesses.save", "", fmt.Sprintf("доступов %d", len(list)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accesses": accessFormEntries(list)})
}

// MigrateAccessPasswords пересчитывает пароли доступов в хеши: раньше они
// лежали на диске как есть. Вызывается один раз при старте сервера — иначе
// открытые пароли остались бы в файлах до ближайшей правки каждой группы.
// Старые записи продолжают работать и без миграции (см. domain.VerifyPassword),
// так что упасть здесь не на чем: ошибки только логируются.
func (h *Handlers) MigrateAccessPasswords() {
	hashList := func(list []domain.AccessEntry) ([]domain.AccessEntry, int) {
		changed := 0
		for i := range list {
			e := &list[i]
			if e.Auth != domain.AccessAuthPassword || e.Password == "" || domain.IsHashedPassword(e.Password) {
				continue
			}
			hashed, err := domain.HashPassword(e.Password)
			if err != nil {
				h.logger.Printf("WARN hash password for access %q: %v", e.Title, err)
				continue
			}
			e.Password = hashed
			changed++
		}
		return list, changed
	}

	total := 0
	for _, slug := range h.allGroupSlugs() {
		gf, ok, err := h.readGroupFile(slug)
		if err != nil || !ok || len(gf.Accesses) == 0 {
			continue
		}
		list, changed := hashList(gf.Accesses)
		if changed == 0 {
			continue
		}
		gf.Accesses = list
		if err := h.writeGroupFile(slug, gf); err != nil {
			h.logger.Printf("WARN save hashed passwords for group %s: %v", slug, err)
			continue
		}
		h.logger.Printf("INFO group=%s: паролей доступов переведено в хеш: %d", slug, changed)
		total += changed
	}

	// Глобальные доступы: файла может не быть вовсе (тогда loadGlobalAccesses
	// отдаёт виртуальную запись легаси-каталога) — создавать его не нужно.
	if path := h.globalAccessesPath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			list, changed := hashList(h.loadGlobalAccesses())
			if changed > 0 {
				if err := h.saveGlobalAccesses(list); err != nil {
					h.logger.Printf("WARN save hashed global passwords: %v", err)
				} else {
					h.logger.Printf("INFO глобальных паролей переведено в хеш: %d", changed)
					total += changed
				}
			}
		}
	}

	if total > 0 {
		h.audit(nil, auditEntry{Actor: "сервер", Kind: "admin", Action: "accesses.rehash",
			Detail: fmt.Sprintf("паролей переведено в хеш: %d", total), OK: true})
	}
}
