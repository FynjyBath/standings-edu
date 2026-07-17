package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// resultsActiveSince сообщает, есть ли у аккаунта кэшированные посылки не старше
// since. Используется для «сброса за период»: чистим только аккаунты, у которых
// была активность в этом окне.
func resultsActiveSince(results []TaskResult, since time.Time) bool {
	for _, r := range results {
		for _, t := range r.Timed {
			if !t.At.Before(since) {
				return true
			}
		}
	}
	return false
}

// accountMatchSet нормализует список идентификаторов (trim + нижний регистр).
// nil на входе означает «все аккаунты» и возвращает nil.
func accountMatchSet(ids []string) map[string]struct{} {
	if ids == nil {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func matchesAccount(set map[string]struct{}, id string) bool {
	if set == nil {
		return true
	}
	_, ok := set[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// writeStateFileAtomic атомарно перезаписывает файл состояния (temp + rename).
func writeStateFileAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// ClearInformaticsCache удаляет из файла состояния informatics записи аккаунтов,
// попавших под фильтр (accounts==nil — все). Если activeSince не нулевой, удаляются
// только аккаунты с посылками не старше него (сброс «за период»). Удалённая запись
// заставит ближайшую генерацию перечитать аккаунт с нуля. Возвращает число
// сброшенных аккаунтов; отсутствие файла — не ошибка.
func ClearInformaticsCache(statePath string, accounts []string, activeSince time.Time) (int, error) {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return 0, nil
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var file informaticsStateFile
	if err := json.Unmarshal(b, &file); err != nil {
		return 0, fmt.Errorf("decode informatics state %q: %w", statePath, err)
	}
	if len(file.Accounts) == 0 {
		return 0, nil
	}

	set := accountMatchSet(accounts)
	removed := 0
	for id, acc := range file.Accounts {
		if !matchesAccount(set, id) {
			continue
		}
		if activeSince.IsZero() || storedRunsActiveSince(acc.Runs, activeSince) {
			delete(file.Accounts, id)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if err := writeStateFileAtomic(statePath, file); err != nil {
		return 0, err
	}
	return removed, nil
}

// storedRunsActiveSince — есть ли у аккаунта сохранённые посылки не старше since.
func storedRunsActiveSince(runs []informaticsStoredRun, since time.Time) bool {
	for _, r := range runs {
		if !r.At.IsZero() && !r.At.Before(since) {
			return true
		}
	}
	return false
}

// ClearCodeforcesCache — то же для codeforces (ключ — handle, регистронезависимо).
func ClearCodeforcesCache(statePath string, handles []string, activeSince time.Time) (int, error) {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return 0, nil
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var file codeforcesStateFile
	if err := json.Unmarshal(b, &file); err != nil {
		return 0, fmt.Errorf("decode codeforces state %q: %w", statePath, err)
	}
	if len(file.Accounts) == 0 {
		return 0, nil
	}

	set := accountMatchSet(handles)
	removed := 0
	for id, acc := range file.Accounts {
		if !matchesAccount(set, id) {
			continue
		}
		if activeSince.IsZero() || resultsActiveSince(acc.Results, activeSince) {
			delete(file.Accounts, id)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if err := writeStateFileAtomic(statePath, file); err != nil {
		return 0, err
	}
	return removed, nil
}
