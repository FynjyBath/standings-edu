package httpapi

// Глобальные доступы: те же записи, что у группы, но живут в
// data/credentials/global_accesses.json и действуют на все группы или на
// выбранные (scope). Ими же выдаётся каталог групп (право view.directory).
//
// Файл лежит среди прочих секретов: в бандл экспорта не попадает.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

// globalAccessesPath — файл глобальных доступов ("" — data-каталог не задан).
func (h *Handlers) globalAccessesPath() string {
	if h.dataDir == "" {
		return ""
	}
	return filepath.Join(h.dataDir, "credentials", "global_accesses.json")
}

// loadGlobalAccesses читает глобальные доступы. Файла нет — читается легаси
// (директорный токен) как виртуальный доступ «Каталог».
func (h *Handlers) loadGlobalAccesses() []domain.AccessEntry {
	path := h.globalAccessesPath()
	if path == "" {
		return nil
	}
	var out []domain.AccessEntry
	body, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(body, &out) == nil && len(out) > 0 {
			return out
		}
		if err := json.Unmarshal(body, &out); err != nil {
			h.logger.Printf("WARN read %s: %v", path, err)
		}
		return out
	}
	if !errors.Is(err, os.ErrNotExist) {
		h.logger.Printf("WARN read %s: %v", path, err)
		return nil
	}
	// Легаси: директорный токен давал каталог со ссылками групп.
	if token := h.readDirectoryToken(); token != "" {
		return []domain.AccessEntry{{
			ID: "legacy-directory", Title: "Каталог групп",
			Auth: domain.AccessAuthToken, Token: token,
			Scope:  domain.AccessScopeAll,
			Perms:  []domain.Perm{domain.PermViewDirectory},
			Legacy: true,
		}}
	}
	return nil
}

// saveGlobalAccesses записывает глобальные доступы (файл создаётся при нужде).
func (h *Handlers) saveGlobalAccesses(list []domain.AccessEntry) error {
	path := h.globalAccessesPath()
	if path == "" {
		return errors.New("data dir is not configured")
	}
	if list == nil {
		list = []domain.AccessEntry{}
	}
	return fileutil.WriteJSON(path, list, 0o600)
}

// groupAccesses — доступы конкретной группы (с учётом легаси-полей).
func (h *Handlers) groupAccesses(slug string) []domain.AccessEntry {
	gf, ok := h.readSourceGroupFile(slug)
	if !ok {
		return nil
	}
	return gf.EffectiveAccesses()
}

// accessGroupsFor — слаги групп, которые покрывает глобальный доступ (для
// каталога). scope=all — все настроенные группы в алфавитном порядке.
func (h *Handlers) accessGroupsFor(entry *domain.AccessEntry) []string {
	if entry == nil {
		return nil
	}
	if entry.Scope == domain.AccessScopeGroups {
		out := make([]string, 0, len(entry.Groups))
		for _, g := range entry.Groups {
			g = strings.TrimSpace(g)
			if domain.IsValidSlug(g) {
				out = append(out, g)
			}
		}
		return out
	}
	return h.allGroupSlugs()
}

// allGroupSlugs — слаги всех настроенных групп (data/groups/*).
func (h *Handlers) allGroupSlugs() []string {
	if h.dataDir == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(h.dataDir, "groups"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && domain.IsValidSlug(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}
