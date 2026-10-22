package httpapi

// Страница ученика для родителей: /standings/child?key=<ключ>.
//
// Ключ вычисляется из id ученика по секрету (domain.StudentLinkKey) и нигде не
// хранится: ссылка есть у каждого ученика сразу и не меняется. Секрет лежит в
// data/credentials/student_links_key.json ({"key": "<hex>"}) и сервером НЕ
// создаётся: сгенерированный сам собой секрет при потере файла молча поменял
// бы все розданные ссылки. Нет файла — ссылок нет (страница отвечает 404, а
// преподавателю объясняется, как завести ключ).
//
// На странице — только публичное: те же таблицы, что видит аноним на странице
// группы (без скрытого, без разморозки, без времени задач), и из них — одна
// строка этого ученика. Мест нет: в таблице из одной строки место лишь
// сравнивает ребёнка с остальными. Из закрытой статистики (профиль, темп,
// флаги, лента посылок) не берётся ничего.
//
// Ссылки видит преподаватель с правом view.parent_links — на страницах
// /standings/<slug>/manage/parent-links и в профиле ученика.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"standings-edu/internal/domain"
	"standings-edu/internal/studentintake"
)

// studentLinkPath — адрес страницы ученика без ключа.
const studentLinkPath = "/standings/child"

// errStudentLinksNoKey — файла с секретом нет: ссылки выключены.
var errStudentLinksNoKey = errors.New("ключ ссылок для родителей не настроен")

// studentLinksState — секрет и обратный индекс «ключ → id ученика». Оба
// перечитываются по отпечатку файлов, поэтому положенный на сервер ключ и
// новые ученики подхватываются без перезапуска.
type studentLinksState struct {
	mu      sync.Mutex
	keyPath string

	secretFP  string
	secret    []byte
	secretErr error

	indexFP string
	byKey   map[string]string
}

// ConfigureStudentLinksKeyFile задаёт файл секрета ссылок. Пусто — по
// умолчанию <data>/credentials/student_links_key.json.
func (h *Handlers) ConfigureStudentLinksKeyFile(path string) {
	h.studentLinks.mu.Lock()
	defer h.studentLinks.mu.Unlock()
	h.studentLinks.keyPath = strings.TrimSpace(path)
}

// StudentLinksKeyPath — где сервер ищет секрет ссылок.
func (h *Handlers) StudentLinksKeyPath() string {
	h.studentLinks.mu.Lock()
	defer h.studentLinks.mu.Unlock()
	return h.studentLinksKeyPathLocked()
}

func (h *Handlers) studentLinksKeyPathLocked() string {
	if h.studentLinks.keyPath != "" {
		return h.studentLinks.keyPath
	}
	if h.dataDir == "" {
		return ""
	}
	return filepath.Join(h.dataDir, "credentials", "student_links_key.json")
}

// StudentLinksStatus — nil, если ссылки работают; иначе причина (для лога при
// старте и подсказки на странице).
func (h *Handlers) StudentLinksStatus() error {
	_, err := h.studentLinkSecret()
	return err
}

// studentLinkSecret читает секрет (с кэшем по отпечатку файла).
func (h *Handlers) studentLinkSecret() ([]byte, error) {
	h.studentLinks.mu.Lock()
	defer h.studentLinks.mu.Unlock()
	return h.studentLinkSecretLocked()
}

func (h *Handlers) studentLinkSecretLocked() ([]byte, error) {
	path := h.studentLinksKeyPathLocked()
	if path == "" {
		return nil, errStudentLinksNoKey
	}
	fp, err := fileFingerprint(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errStudentLinksNoKey
		}
		return nil, fmt.Errorf("ключ ссылок для родителей %s: %w", path, err)
	}
	if fp == h.studentLinks.secretFP {
		return h.studentLinks.secret, h.studentLinks.secretErr
	}
	secret, parseErr := readStudentLinkSecret(path)
	h.studentLinks.secretFP = fp
	h.studentLinks.secret = secret
	h.studentLinks.secretErr = parseErr
	h.studentLinks.indexFP = "" // индекс построен на прежнем секрете
	return secret, parseErr
}

// readStudentLinkSecret разбирает файл секрета: {"key": "<hex>"}.
func readStudentLinkSecret(path string) ([]byte, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ключ ссылок для родителей %s: %w", path, err)
	}
	var cfg struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("ключ ссылок для родителей %s: ожидается {\"key\": \"<hex>\"}: %w", path, err)
	}
	secret, err := hex.DecodeString(strings.TrimSpace(cfg.Key))
	if err != nil {
		return nil, fmt.Errorf("ключ ссылок для родителей %s: key должен быть в hex: %w", path, err)
	}
	if len(secret) < domain.MinStudentLinkSecretBytes {
		return nil, fmt.Errorf("ключ ссылок для родителей %s: нужно не меньше %d байт (%d hex-символов)",
			path, domain.MinStudentLinkSecretBytes, 2*domain.MinStudentLinkSecretBytes)
	}
	return secret, nil
}

// fileFingerprint — размер и время изменения файла (для инвалидации кэшей).
func fileFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d|%d", info.Size(), info.ModTime().UnixNano()), nil
}

// studentLinkURL — относительная ссылка на страницу ученика ("" — ссылки
// выключены).
func (h *Handlers) studentLinkURL(studentID string) string {
	secret, err := h.studentLinkSecret()
	if err != nil {
		return ""
	}
	key := domain.StudentLinkKey(secret, studentID)
	if key == "" {
		return ""
	}
	return studentLinkPath + "?key=" + url.QueryEscape(key)
}

// studentByLinkKey — id ученика по ключу ссылки. Ключ необратим, поэтому
// ключи считаются для всех учеников; индекс кэшируется до смены
// students.json или секрета.
func (h *Handlers) studentByLinkKey(key string) (string, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) != 32 {
		return "", false
	}
	if _, err := hex.DecodeString(key); err != nil {
		return "", false
	}

	h.studentLinks.mu.Lock()
	defer h.studentLinks.mu.Unlock()
	secret, err := h.studentLinkSecretLocked()
	if err != nil || h.dataDir == "" {
		return "", false
	}
	studentsPath := filepath.Join(h.dataDir, "students.json")
	fp, err := fileFingerprint(studentsPath)
	if err != nil {
		return "", false
	}
	fp += "|" + h.studentLinks.secretFP
	if fp != h.studentLinks.indexFP {
		students, err := studentintake.LoadStudentsFile(studentsPath)
		if err != nil {
			h.logger.Printf("WARN student links: read students: %v", err)
			return "", false
		}
		byKey := make(map[string]string, len(students))
		for _, s := range students {
			if id := domain.NormalizeID(s.ID); id != "" {
				byKey[domain.StudentLinkKey(secret, id)] = id
			}
		}
		h.studentLinks.byKey = byKey
		h.studentLinks.indexFP = fp
	}
	id, ok := h.studentLinks.byKey[key]
	return id, ok
}

// ── Страница для родителей ──────────────────────────────────────────────────

// StudentChildPageData — страница ученика для родителей.
type StudentChildPageData struct {
	PageTitle  string
	Footer     FooterInfo
	PublicName string
	Groups     []StudentChildGroup
}

// StudentChildGroup — одна группа ученика: его строки доски почёта и оценок и
// контесты, в которых он участвовал (в каждом — только его строка).
type StudentChildGroup struct {
	Slug     string
	Title    string
	Archived bool
	// HonorSites/Honor — строка доски почёта (решено задач по сайтам).
	HonorSites []string
	Honor      *domain.GeneratedGroupSolvedSummaryRow
	// Grades — таблица оценок, в Rows только строка ученика; nil — оценок нет.
	Grades   *domain.GeneratedGrades
	Contests []domain.GeneratedContestStandings
}

// StudentChildPage — страница ученика по ключу ссылки. Неизвестный ключ — 404,
// как будто страницы нет: по ответу нельзя понять, бывает ли такой ключ.
func (h *Handlers) StudentChildPage(w http.ResponseWriter, r *http.Request) {
	// Ключ в адресе — пропуск к странице: не отдаём его сайтам задач в
	// Referer, не даём индексировать и кэшировать.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")

	id, ok := h.studentByLinkKey(r.URL.Query().Get("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	name := id
	if s, found := h.loadStudentsByIDFromDataDir()[id]; found {
		if n := strings.TrimSpace(s.PublicName); n != "" {
			name = n
		} else if n := strings.TrimSpace(s.FullName); n != "" {
			name = n
		}
	}
	page := StudentChildPageData{
		PageTitle:  name + " — результаты",
		Footer:     h.buildFooterInfo(),
		PublicName: name,
		Groups:     h.studentChildGroups(id),
	}
	if err := h.renderer.Render(w, http.StatusOK, "student_child.html", page); err != nil {
		h.logger.Printf("ERROR render student child page id=%s err=%v", id, err)
	}
}

// loadStudentsByIDFromDataDir — ученики по id из data/students.json (без
// зависимости от админки: страница публичная).
func (h *Handlers) loadStudentsByIDFromDataDir() map[string]domain.Student {
	out := make(map[string]domain.Student)
	if h.dataDir == "" {
		return out
	}
	students, err := studentintake.LoadStudentsFile(filepath.Join(h.dataDir, "students.json"))
	if err != nil {
		return out
	}
	for _, s := range domain.NormalizeStudents(students) {
		out[s.ID] = s
	}
	return out
}

// studentChildGroups собирает группы ученика. Объединённые группы пропускаются
// (их контесты — это контесты групп-участниц). Активные группы идут раньше
// архивных: общий контест показывается ОДИН раз — в первой группе, где у
// ученика по нему есть результат.
func (h *Handlers) studentChildGroups(id string) []StudentChildGroup {
	type candidate struct {
		slug, title string
		archived    bool
	}
	cands := make([]candidate, 0)
	for _, slug := range h.allGroupSlugs() {
		gf, ok := h.readSourceGroupFile(slug)
		if !ok || len(gf.MemberGroups) > 0 {
			continue
		}
		member := false
		for _, sid := range domain.NormalizeGroups(gf.StudentIDs) {
			if sid == id {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		title := strings.TrimSpace(gf.Title)
		if title == "" {
			title = slug
		}
		cands = append(cands, candidate{slug: slug, title: title, archived: gf.Archived()})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].archived != cands[j].archived {
			return !cands[i].archived
		}
		if ti, tj := strings.ToLower(cands[i].title), strings.ToLower(cands[j].title); ti != tj {
			return ti < tj
		}
		return cands[i].slug < cands[j].slug
	})

	shown := make(map[string]struct{})
	out := make([]StudentChildGroup, 0, len(cands))
	for _, c := range cands {
		standings, err := h.loadGroupStandings(c.slug)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				h.logger.Printf("WARN student child page: group=%s load: %v", c.slug, err)
			}
			continue
		}
		// Ровно публичный вид: доступ без прав.
		h.applyAccessView(&standings, c.slug, &GroupAccess{Perms: domain.PermSet{}})

		group := StudentChildGroup{Slug: c.slug, Title: c.title, Archived: c.archived}
		for i := range standings.SolvedSummary {
			if standings.SolvedSummary[i].StudentID == id {
				row := standings.SolvedSummary[i]
				group.Honor = &row
				group.HonorSites = standings.SolvedSummarySites
				break
			}
		}
		if g := standings.Grades; g != nil {
			for _, row := range g.Rows {
				if row.StudentID == id {
					only := *g
					only.Rows = []domain.GeneratedGradeRow{row}
					group.Grades = &only
					break
				}
			}
		}
		for _, contest := range standings.Contests {
			if _, dup := shown[contest.ID]; dup {
				continue
			}
			for _, row := range contest.Rows {
				if row.StudentID != id {
					continue
				}
				if row.Participated() {
					one := contest
					one.Rows = []domain.GeneratedRow{row}
					one.RowsFull = nil
					group.Contests = append(group.Contests, one)
					shown[contest.ID] = struct{}{}
				}
				break
			}
		}
		if group.Honor == nil && group.Grades == nil && len(group.Contests) == 0 {
			continue
		}
		out = append(out, group)
	}
	return out
}

// ── Ссылки для преподавателя ────────────────────────────────────────────────

// ParentLinksPageData — список ссылок на страницы учеников группы.
type ParentLinksPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	RoleTitle  string
	BackURL    string
	BackLabel  string
	// KeyError — ссылки выключены (нет или битый ключ): объясняем, как завести.
	KeyError string
	KeyPath  string
	Rows     []ParentLinkRow
}

// ParentLinkRow — ученик группы и ссылка на его страницу.
type ParentLinkRow struct {
	StudentID  string
	PublicName string
	FullName   string
	URL        string
}

// GroupParentLinksPage — ссылки на страницы учеников группы (право
// view.parent_links).
func (h *Handlers) GroupParentLinksPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	acc, ok := h.signInToGroup(w, r, slug)
	if !ok {
		return
	}
	if !acc.Has(domain.PermViewParentLinks) {
		http.Error(w, "нет права смотреть ссылки для родителей", http.StatusForbidden)
		return
	}
	h.renderParentLinksPage(w, r, slug, acc.Title(), "/standings/"+slug+h.tokenQuery(acc.Token), "← Таблицы группы")
}

// AdminGroupParentLinksPage — то же из админки (?slug=…).
func (h *Handlers) AdminGroupParentLinksPage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if !domain.IsValidSlug(slug) {
		http.NotFound(w, r)
		return
	}
	h.renderParentLinksPage(w, r, slug, "", "/standings/admin/group?slug="+url.QueryEscape(slug), "← Группа в админке")
}

func (h *Handlers) renderParentLinksPage(w http.ResponseWriter, r *http.Request, slug, roleTitle, backURL, backLabel string) {
	gf, found := h.readSourceGroupFile(slug)
	if !found {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(gf.Title)
	if title == "" {
		title = slug
	}
	page := ParentLinksPageData{
		PageTitle:  title + " — ссылки для родителей",
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		RoleTitle:  roleTitle,
		BackURL:    backURL,
		BackLabel:  backLabel,
		KeyPath:    h.StudentLinksKeyPath(),
	}
	if err := h.StudentLinksStatus(); err != nil {
		page.KeyError = err.Error()
	}
	byID := h.loadStudentsByIDFromDataDir()
	for _, id := range h.resolveGroupStudentIDs(slug) {
		s := byID[id]
		row := ParentLinkRow{StudentID: id, PublicName: strings.TrimSpace(s.PublicName), FullName: strings.TrimSpace(s.FullName)}
		if row.PublicName == "" {
			row.PublicName = id
		}
		if page.KeyError == "" {
			row.URL = h.studentLinkURL(id)
		}
		page.Rows = append(page.Rows, row)
	}
	sort.SliceStable(page.Rows, func(i, j int) bool {
		return strings.ToLower(page.Rows[i].PublicName) < strings.ToLower(page.Rows[j].PublicName)
	})
	w.Header().Set("Cache-Control", "no-store")
	if err := h.renderer.Render(w, http.StatusOK, "group_parent_links.html", page); err != nil {
		h.logger.Printf("ERROR render parent links slug=%s: %v", slug, err)
	}
}
