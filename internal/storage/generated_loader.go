package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

// standingsCacheEntry — распарсенные standings группы и «отпечаток» файла, по
// которому определяется устаревание кэша.
type standingsCacheEntry struct {
	modTime time.Time
	size    int64
	value   domain.GeneratedGroupStandings
}

type GeneratedLoader struct {
	OutDir string

	// cache хранит распарсенные standings по slug. Ключ актуальности — mtime+size
	// файла: generate пишет файлы атомарно (temp+rename) с новым mtime, поэтому
	// сервер (отдельный процесс) подхватывает обновления на следующем запросе.
	cacheMu sync.RWMutex
	cache   map[string]standingsCacheEntry
}

var ErrInvalidGroupSlug = errors.New("invalid group slug")

func NewGeneratedLoader(outDir string) *GeneratedLoader {
	return &GeneratedLoader{
		OutDir: outDir,
		cache:  make(map[string]standingsCacheEntry),
	}
}

func (l *GeneratedLoader) LoadGroups() ([]domain.GeneratedGroupMeta, error) {
	path := filepath.Join(l.OutDir, "groups.json")
	var groups []domain.GeneratedGroupMeta
	if err := fileutil.ReadJSON(path, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (l *GeneratedLoader) LoadGroupStandings(slug string) (domain.GeneratedGroupStandings, error) {
	if !domain.IsValidSlug(slug) {
		return domain.GeneratedGroupStandings{}, ErrInvalidGroupSlug
	}

	path := filepath.Join(l.OutDir, "standings", slug+".json")

	// Stat до чтения: если файл заменят между stat и чтением, содержимое будет
	// не старее «отпечатка», поэтому устаревшее в кэш не попадёт — следующий
	// запрос увидит новый mtime и перечитает.
	info, err := os.Stat(path)
	if err != nil {
		l.cacheMu.Lock()
		delete(l.cache, slug)
		l.cacheMu.Unlock()
		return domain.GeneratedGroupStandings{}, err
	}

	l.cacheMu.RLock()
	ent, ok := l.cache[slug]
	l.cacheMu.RUnlock()
	if ok && ent.size == info.Size() && ent.modTime.Equal(info.ModTime()) {
		return ent.value.CloneForServe(), nil
	}

	var standings domain.GeneratedGroupStandings
	if err := fileutil.ReadJSON(path, &standings); err != nil {
		return domain.GeneratedGroupStandings{}, err
	}

	l.cacheMu.Lock()
	l.cache[slug] = standingsCacheEntry{modTime: info.ModTime(), size: info.Size(), value: standings}
	l.cacheMu.Unlock()

	// Клон, а не сам кэшированный value: вызывающий мутирует результат (скрытие
	// ссылок, подмена full-версий), кэш должен остаться нетронутым.
	return standings.CloneForServe(), nil
}

func (l *GeneratedLoader) LoadLastUpdatedAt() (time.Time, error) {
	candidates := []string{
		filepath.Join(l.OutDir, "groups.json"),
	}

	latest := time.Time{}
	found := false

	for _, p := range candidates {
		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return time.Time{}, err
		}
		if !found || info.ModTime().After(latest) {
			latest = info.ModTime()
			found = true
		}
	}

	standingsDir := filepath.Join(l.OutDir, "standings")
	entries, err := os.ReadDir(standingsDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return time.Time{}, err
		}
	} else {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
				continue
			}
			info, statErr := os.Stat(filepath.Join(standingsDir, e.Name()))
			if statErr != nil {
				continue
			}
			if !found || info.ModTime().After(latest) {
				latest = info.ModTime()
				found = true
			}
		}
	}

	if !found {
		return time.Time{}, os.ErrNotExist
	}
	return latest, nil
}
