package httpapi

// Порядок глобальных контестов (data/contests.json): перестановка соседей и
// сортировка по id. Порядок ни на что в генерации не влияет — это порядок
// показа в админке и в списках «добавить в группу», где искать удобнее в
// предсказуемом списке.
//
// Работаем с сырыми элементами массива: переставлять записи нужно, ничего в
// них не меняя, поэтому через domain.Contest их не прогоняем.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	"standings-edu/internal/fileutil"
)

// loadContestsRaw читает contests.json как массив сырых элементов.
func (h *Handlers) loadContestsRaw() ([]json.RawMessage, error) {
	var raw []json.RawMessage
	if err := fileutil.ReadJSON(h.dataPath("contests.json"), &raw); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// writeContestsRaw перезаписывает contests.json сырыми элементами.
func (h *Handlers) writeContestsRaw(items []json.RawMessage) error {
	if items == nil {
		items = []json.RawMessage{}
	}
	return fileutil.WriteJSON(h.dataPath("contests.json"), items, 0o644)
}

// rawContestID достаёт id из сырого элемента ("" — не объект или без id).
func rawContestID(item json.RawMessage) string {
	var meta struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(item, &meta) != nil {
		return ""
	}
	return strings.TrimSpace(meta.ID)
}

// AdminContestMove переставляет глобальный контест на одну позицию.
func (h *Handlers) AdminContestMove(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		ID  string `json:"id"`
		Dir string `json:"dir"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	id, dir := strings.TrimSpace(req.ID), strings.TrimSpace(req.Dir)
	if id == "" || (dir != "up" && dir != "down") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "нужны id и направление (up/down)"})
		return
	}
	items, err := h.loadContestsRaw()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	idx := -1
	for i, item := range items {
		if rawContestID(item) == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "контест не найден"})
		return
	}
	swap := idx - 1
	if dir == "down" {
		swap = idx + 1
	}
	if swap < 0 || swap >= len(items) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "контест уже с краю"})
		return
	}
	items[idx], items[swap] = items[swap], items[idx]
	if err := h.writeContestsRaw(items); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AdminContestsSort сортирует глобальные контесты по id.
func (h *Handlers) AdminContestsSort(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	items, err := h.loadContestsRaw()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		return naturalLess(rawContestID(items[i]), rawContestID(items[j]))
	})
	if err := h.writeContestsRaw(items); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(items)})
}

// naturalLess — сравнение id «по-человечески»: числа внутри строки сравниваются
// как числа, поэтому week_9 идёт перед week_10, а не наоборот. Регистр не
// важен, при полном совпадении — обычное сравнение, чтобы порядок был строгим.
func naturalLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(la) && j < len(lb) {
		ca, cb := la[i], lb[j]
		if isDigit(ca) && isDigit(cb) {
			si, sj := i, j
			for i < len(la) && isDigit(la[i]) {
				i++
			}
			for j < len(lb) && isDigit(lb[j]) {
				j++
			}
			// Ведущие нули не меняют значение числа, но различают строки.
			na := strings.TrimLeft(la[si:i], "0")
			nb := strings.TrimLeft(lb[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	if len(la)-i != len(lb)-j {
		return len(la)-i < len(lb)-j
	}
	return a < b
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
