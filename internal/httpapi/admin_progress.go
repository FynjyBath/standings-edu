package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Прогресс выполняющегося действия и история завершённых.
//
// Действия запускаются в фоне, а страница показывает их состояние при обычном
// обновлении: без опроса с клиента, без вебсокетов, без нагрузки на сервер
// между заходами. Прогресс берётся из вывода самой команды — генератор печатает
// строки вида «PROGRESS stage=accounts done=17 total=210», а родитель их
// перехватывает и в показанный вывод не пропускает, чтобы не засорять его
// сотнями строк.

// adminHistoryLimit — сколько завершённых действий помнить. Держится в памяти:
// история нужна, чтобы понять «что я только что натворил», а не как архив —
// для архива есть журнал.
const adminHistoryLimit = 12

// AdminActionProgress — как идёт действие, выполняющееся прямо сейчас.
type AdminActionProgress struct {
	Action    string    `json:"action"`
	StartedAt time.Time `json:"started_at"`
	// Stage — какой этап идёт; Done/Total — сколько его единиц пройдено.
	Stage string `json:"stage,omitempty"`
	Done  int    `json:"done,omitempty"`
	Total int    `json:"total,omitempty"`
	// Note — что обрабатывается прямо сейчас (группа, сайт). Когда этап
	// подвисает, по ней видно, на чём именно.
	Note string `json:"note,omitempty"`
	// UpdatedAt — когда прогресс последний раз сдвинулся. По нему видно, что
	// этап стоит: полоса «165 из 165» сама по себе не отличает «только что
	// закончили» от «висим десять минут».
	UpdatedAt time.Time `json:"updated_at"`
}

// Percent — доля выполненного, 0..100. Ноль, если общее число неизвестно.
func (p AdminActionProgress) Percent() int {
	if p.Total <= 0 {
		return 0
	}
	v := p.Done * 100 / p.Total
	if v > 100 {
		return 100
	}
	return v
}

// Known — есть ли что показывать в виде «столько из стольких».
func (p AdminActionProgress) Known() bool { return p.Total > 0 }

// StageTitle — человеческое название этапа.
func (p AdminActionProgress) StageTitle() string {
	switch p.Stage {
	case "accounts":
		return "опрашиваю аккаунты на сайтах"
	case "tables":
		return "собираю таблицы групп"
	case "profiles":
		return "считаю профили учеников"
	case "tempo":
		return "считаю темп курса"
	case "review":
		return "собираю очередь проверки оценок"
	case "write":
		return "записываю файлы"
	default:
		return "выполняется"
	}
}

// stalledAfter — после какого молчания считаем, что этап подозрительно встал.
// Опрос аккаунтов отчитывается часто, сборка таблиц — раз в группу; две минуты
// без движения означают либо очень медленный сайт, либо затык.
const stalledAfter = 2 * time.Minute

// Stalled — прогресс давно не двигался.
func (p AdminActionProgress) Stalled() bool {
	return time.Since(p.UpdatedAt) > stalledAfter
}

// StalledFor — сколько прогресс стоит на месте, словами.
func (p AdminActionProgress) StalledFor() string {
	d := time.Since(p.UpdatedAt).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

// Elapsed — сколько уже идёт, словами.
func (p AdminActionProgress) Elapsed() string {
	d := time.Since(p.StartedAt).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

// progressWriter перехватывает строки PROGRESS из вывода команды: считает их
// прогрессом и НЕ пропускает дальше. Всё остальное уходит в вывод как есть.
//
// Writer вызывается из горутины, копирующей вывод процесса, поэтому состояние
// под замком: страницу могут обновить в любой момент.
type progressWriter struct {
	out io.Writer
	buf bytes.Buffer
	on  func(stage string, done, total int, note string)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Хвост без перевода строки — возвращаем в буфер до следующего раза.
			w.buf.Reset()
			w.buf.WriteString(line)
			break
		}
		if stage, done, total, note, ok := parseProgressLine(line); ok {
			if w.on != nil {
				w.on(stage, done, total, note)
			}
			continue
		}
		if _, werr := io.WriteString(w.out, line); werr != nil {
			return n, werr
		}
	}
	return n, nil
}

// Flush дописывает недописанную последнюю строку.
func (w *progressWriter) Flush() {
	if w.buf.Len() == 0 {
		return
	}
	line := w.buf.String()
	w.buf.Reset()
	if _, _, _, _, ok := parseProgressLine(line); ok {
		return
	}
	_, _ = io.WriteString(w.out, line)
}

// parseProgressLine разбирает «… PROGRESS stage=accounts done=17 total=210».
// Префикс логгера (дата, время) игнорируется — ищем маркер где угодно в строке.
func parseProgressLine(line string) (stage string, done, total int, note string, ok bool) {
	i := strings.Index(line, "PROGRESS ")
	if i < 0 {
		return "", 0, 0, "", false
	}
	for _, field := range strings.Fields(line[i+len("PROGRESS "):]) {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch key {
		case "stage":
			stage = value
		case "done":
			done, _ = strconv.Atoi(value)
		case "total":
			total, _ = strconv.Atoi(value)
		case "note":
			note = value
		}
	}
	return stage, done, total, note, stage != "" || total > 0
}

// FormatProgress — единая точка формирования строки прогресса, чтобы
// отправитель и получатель не разъехались.
func FormatProgress(stage string, done, total int, note string) string {
	if note == "" {
		return fmt.Sprintf("PROGRESS stage=%s done=%d total=%d", stage, done, total)
	}
	return fmt.Sprintf("PROGRESS stage=%s done=%d total=%d note=%s", stage, done, total, note)
}

// ── Состояние ────────────────────────────────────────────────────────────────

type adminProgressState struct {
	mu      sync.RWMutex
	current *AdminActionProgress
}

func (s *adminProgressState) start(action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.current = &AdminActionProgress{Action: action, StartedAt: now, UpdatedAt: now}
}

func (s *adminProgressState) update(stage string, done, total int, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return
	}
	// Смена этапа сбрасывает счётчик: иначе полоса застывала бы на «165 из 165,
	// 100%» всё время, пока идёт следующий, ещё не отчитавшийся этап.
	s.current.Stage = stage
	s.current.Done = done
	s.current.Total = total
	s.current.Note = note
	s.current.UpdatedAt = time.Now()
}

func (s *adminProgressState) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = nil
}

func (s *adminProgressState) snapshot() *AdminActionProgress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil
	}
	copied := *s.current
	return &copied
}
