package httpapi

import (
	"encoding/json"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/source"
)

// Таблицы оценок кондуитов хранятся отдельно от определений контестов:
// data/manual_tables.json (для глобальных) и data/groups/<slug>/manual_tables.json
// (для inline-кондуитов группы), map contest_id -> TSV. В contests.json остаётся
// только определение (task_count, show_all …). Старый формат (table внутри
// provider_config) читается как fallback и мигрирует при первом сохранении.

func (h *Handlers) globalManualTablesPath() string {
	return h.dataPath(source.ManualTablesFileName)
}

func (h *Handlers) groupManualTablesPath(slug string) string {
	return h.dataPath("groups", slug, source.ManualTablesFileName)
}

// loadManualTablesFile читает map contest_id -> таблица (нет файла — пустая).
func loadManualTablesFile(path string) map[string]string {
	tables := map[string]string{}
	if err := fileutil.ReadJSON(path, &tables); err != nil {
		return map[string]string{}
	}
	return tables
}

// setManualTablesEntry записывает/обновляет таблицу контеста в файле.
func setManualTablesEntry(path, id, table string) error {
	tables := loadManualTablesFile(path)
	tables[id] = table
	return fileutil.WriteJSON(path, tables, 0o644)
}

// removeManualTablesEntry убирает запись (удаление контеста). Отсутствие
// записи — не ошибка.
func removeManualTablesEntry(path, id string) error {
	tables := loadManualTablesFile(path)
	if _, ok := tables[id]; !ok {
		return nil
	}
	delete(tables, id)
	return fileutil.WriteJSON(path, tables, 0o644)
}

// renameManualTablesEntry переносит запись под новый id (переименование контеста).
func renameManualTablesEntry(path, oldID, newID string) error {
	if oldID == newID {
		return nil
	}
	tables := loadManualTablesFile(path)
	t, ok := tables[oldID]
	if !ok {
		return nil
	}
	delete(tables, oldID)
	tables[newID] = t
	return fileutil.WriteJSON(path, tables, 0o644)
}

// manualTableFor возвращает таблицу кондуита: запись из manual_tables.json
// приоритетнее таблицы, оставшейся в provider_config (легаси).
func manualTableFor(path, id string, cfg map[string]any) string {
	if t, ok := loadManualTablesFile(path)[id]; ok {
		return t
	}
	t, _ := cfg["table"].(string)
	return t
}

// splitContestManualTable для кондуита выносит таблицу из ProviderConfig в
// файл рядом с contests.json; в конфиге остаётся определение (с гарантированным
// task_count, чтобы пустой конфиг оставался валидным). Не-кондуиты не трогает.
func (h *Handlers) splitContestManualTable(c *domain.Contest, tablesPath string) error {
	if strings.TrimSpace(c.Provider) != source.ManualTableProviderID {
		return nil
	}
	cfg, table, hadTable, err := source.StripManualTable(c.ProviderConfig)
	if err != nil {
		return err
	}
	// task_count обязателен, когда таблицы в конфиге нет: выводим из таблицы.
	cfg, err = ensureManualTaskCount(cfg, table)
	if err != nil {
		return err
	}
	c.ProviderConfig = cfg
	// Конфиг без ключа table (правка сырым JSON) не затирает сохранённые оценки.
	if !hadTable {
		return nil
	}
	return setManualTablesEntry(tablesPath, strings.TrimSpace(c.ID), table)
}

// ensureManualTaskCount добавляет task_count в конфиг кондуита, если он не
// задан: по ширине таблицы (сколько колонок распознано).
func ensureManualTaskCount(cfg []byte, table string) ([]byte, error) {
	m := map[string]any{}
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &m); err != nil {
			return nil, err
		}
	}
	if v, ok := m["task_count"].(float64); ok && v > 0 {
		return cfg, nil
	}
	labels, _ := source.SplitManualTable(table, 0)
	m["task_count"] = len(labels)
	return json.Marshal(m)
}
