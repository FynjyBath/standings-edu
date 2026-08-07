package httpapi

// Журнал действий: кто входил и что менял. Пишется отдельно от общего лога
// сервера — в data/logs/audit.log, по строке JSON на событие (удобно
// фильтровать и не ломается на многострочных значениях).
//
// Логируются входы (успешные и неудачные) и ИЗМЕНЯЮЩИЕ действия — и от доступов
// групп, и от главной админки. Чтение не логируем: журнал утонул бы в шуме.
// Ошибка записи никогда не роняет операцию — только WARN в общий лог.

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// auditMaxBytes — при превышении файл уезжает в audit.log.1 (хранится один).
	auditMaxBytes = 5 << 20
	// auditTailLimit — сколько последних записей отдаёт просмотр.
	auditTailLimit = 500
)

// auditEntry — одна запись журнала.
type auditEntry struct {
	At     string `json:"at"`
	Actor  string `json:"actor"`            // название доступа или «админ»
	Kind   string `json:"kind,omitempty"`   // access | admin
	Group  string `json:"group,omitempty"`  // слаг группы (пусто — глобальное)
	Action string `json:"action"`           // contests.move, grades.manual.save, …
	Detail string `json:"detail,omitempty"` // человекочитаемые подробности
	IP     string `json:"ip,omitempty"`     //
	OK     bool   `json:"ok"`               //
	Error  string `json:"error,omitempty"`  //
}

var auditMu sync.Mutex

// auditPath — файл журнала ("" — data-каталог не задан, журнал выключен).
func (h *Handlers) auditPath() string {
	if h.dataDir == "" {
		return ""
	}
	return filepath.Join(h.dataDir, "logs", "audit.log")
}

// audit пишет событие. Поля At/IP/Kind заполняются сами, если пусты.
func (h *Handlers) audit(r *http.Request, e auditEntry) {
	path := h.auditPath()
	if path == "" {
		return
	}
	if e.At == "" {
		e.At = time.Now().Format(time.RFC3339)
	}
	if e.IP == "" && r != nil {
		e.IP = clientIP(r)
	}
	if e.Kind == "" {
		e.Kind = "access"
	}
	if e.Actor == "" {
		e.Actor = "—"
	}
	blob, err := json.Marshal(e)
	if err != nil {
		return
	}

	auditMu.Lock()
	defer auditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.logger.Printf("WARN audit mkdir: %v", err)
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() > auditMaxBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			h.logger.Printf("WARN audit rotate: %v", err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		h.logger.Printf("WARN audit open: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(blob, '\n')); err != nil {
		h.logger.Printf("WARN audit write: %v", err)
	}
}

// auditAccess — событие от доступа группы (подставляет название доступа).
func (h *Handlers) auditAccess(r *http.Request, acc *GroupAccess, slug, action, detail string) {
	h.audit(r, auditEntry{
		Actor: acc.Title(), Kind: "access", Group: slug,
		Action: action, Detail: detail, OK: true,
	})
}

// auditAccessResult — то же, но с итогом операции: пустое errMsg — успех.
func (h *Handlers) auditAccessResult(r *http.Request, acc *GroupAccess, slug, action, detail, errMsg string) {
	h.audit(r, auditEntry{
		Actor: acc.Title(), Kind: "access", Group: slug,
		Action: action, Detail: detail, OK: errMsg == "", Error: errMsg,
	})
}

// auditAdmin — событие от главной админки.
func (h *Handlers) auditAdmin(r *http.Request, action, group, detail string) {
	h.audit(r, auditEntry{
		Actor: "админ", Kind: "admin", Group: group,
		Action: action, Detail: detail, OK: true,
	})
}

// clientIP — адрес клиента с учётом обратного прокси.
func clientIP(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return fwd
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// readAuditTail читает последние записи журнала (сначала свежие). Битые строки
// пропускаются: журнал не должен ломаться из-за одной кривой записи.
func (h *Handlers) readAuditTail(limit int) []auditEntry {
	path := h.auditPath()
	if path == "" {
		return nil
	}
	if limit <= 0 {
		limit = auditTailLimit
	}
	lines := make([]string, 0, limit*2)
	// Читаем текущий файл, при нехватке — добираем из предыдущего.
	for _, p := range []string{path, path + ".1"} {
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fileLines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
		lines = append(fileLines, lines...)
		if len(lines) >= limit {
			break
		}
	}
	out := make([]auditEntry, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var e auditEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// AdminLogsPageData — страница просмотра журнала.
type AdminLogsPageData struct {
	PageTitle string
	Footer    FooterInfo
	Entries   []AuditRow
	// Path — где лежит файл (чтобы было понятно, что грепать на сервере).
	Path string
}

// AuditRow — запись журнала для показа (время уже разобрано).
type AuditRow struct {
	At     time.Time
	Actor  string
	Kind   string
	Group  string
	Action string
	Detail string
	IP     string
	OK     bool
	Error  string
}

// AdminLogsPage — последние записи журнала (входы и изменения).
func (h *Handlers) AdminLogsPage(w http.ResponseWriter, r *http.Request) {
	entries := h.readAuditTail(auditTailLimit)
	rows := make([]AuditRow, 0, len(entries))
	for _, e := range entries {
		row := AuditRow{
			Actor: e.Actor, Kind: e.Kind, Group: e.Group,
			Action: e.Action, Detail: e.Detail, IP: e.IP, OK: e.OK, Error: e.Error,
		}
		if at, err := time.Parse(time.RFC3339, e.At); err == nil {
			row.At = at
		}
		rows = append(rows, row)
	}
	page := AdminLogsPageData{
		PageTitle: "Журнал действий",
		Footer:    h.buildFooterInfo(),
		Entries:   rows,
		Path:      h.auditPath(),
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := h.renderer.Render(w, http.StatusOK, "admin_logs.html", page); err != nil {
		h.logger.Printf("ERROR render admin logs: %v", err)
	}
}

// AuditAdminWrite — журналирование мутирующих админ-запросов. Отдельные
// хендлеры пишут в журнал сами, когда есть что сказать по делу (какой доступ,
// сколько столбцов); эта обёртка гарантирует, что не потеряется ничего: путь,
// исход и slug/id из тела запроса.
func (h *Handlers) AuditAdminWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail, group := "", ""
		// Тело читаем целиком (админские запросы держатся в памяти и так) и
		// возвращаем на место, чтобы хендлер получил его нетронутым.
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && r.Body != nil {
			if body, err := io.ReadAll(io.LimitReader(r.Body, auditBodyLimit)); err == nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				var fields struct {
					Slug string `json:"slug"`
					ID   string `json:"id"`
					Path string `json:"path"`
				}
				if json.Unmarshal(body, &fields) == nil {
					group = strings.TrimSpace(fields.Slug)
					for _, part := range []string{fields.ID, fields.Path} {
						if part = strings.TrimSpace(part); part != "" {
							detail = strings.TrimSpace(detail + " " + part)
						}
					}
				}
			}
		}
		rec := &auditStatusWriter{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)

		action := strings.TrimPrefix(r.URL.Path, "/api/admin/")
		entry := auditEntry{Actor: "админ", Kind: "admin", Group: group, Action: action, Detail: detail}
		entry.OK = rec.status < http.StatusBadRequest
		if !entry.OK {
			entry.Error = http.StatusText(rec.status)
		}
		h.audit(r, entry)
	}
}

// auditBodyLimit — сколько тела читаем ради slug/id (файлы бывают большими,
// но нужные поля идут в начале объекта… не всегда, поэтому лимит щедрый).
const auditBodyLimit = 8 << 20

// auditStatusWriter запоминает код ответа.
type auditStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditStatusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
