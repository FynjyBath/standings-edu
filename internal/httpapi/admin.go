package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/source"
	"standings-edu/internal/studentintake"
)

const maxAdminJSONBodyBytes = 8 << 20

const (
	adminIntakePath        = "data/student_intake.json"
	adminIntakeStagingPath = "data/student_intake_admin.json"
)

type AdminConfig struct {
	Login        string
	Password     string
	ProjectRoot  string
	DataDir      string
	GeneratedDir string
}

type adminState struct {
	cfg      AdminConfig
	actionMu sync.Mutex

	resultMu   sync.RWMutex
	lastResult *AdminActionResult
}

type AdminActionResult struct {
	Action    string `json:"action"`
	Success   bool   `json:"success"`
	ExitCode  int    `json:"exit_code"`
	StartedAt string `json:"started_at"`
	// StartedAtISO — тот же момент в ISO для показа в поясе браузера (data-localtime).
	StartedAtISO string   `json:"started_at_iso,omitempty"`
	Duration     string   `json:"duration"`
	Output       string   `json:"output"`
	Errors       []string `json:"errors,omitempty"`
}

type AdminPageData struct {
	PageTitle        string
	Footer           FooterInfo
	Editable         []string
	Groups           []AdminGroupLink
	CombinedGroups   []AdminCombinedGroup
	SelectableGroups []AdminGroupLink
	LastResult       *AdminActionResult
	DefaultPath      string
	// Accesses — блок глобальных доступов (тот же редактор, что у группы).
	Accesses AccessEditorData
	// HasArchivedGroups/ArchivedCount — архивные (обычные) группы: свёрнутый
	// блок показываем, только если они есть, и подписываем их числом.
	// HasActiveGroups — есть ли активные: иначе вместо пустого списка объясняем,
	// что всё уехало в архив.
	HasArchivedGroups bool
	ArchivedCount     int
	HasActiveGroups   bool
}

type AdminGroupLink struct {
	Slug       string
	Title      string
	URL        string
	IsCombined bool
	Members    []string
	// Archived — группа в архиве (update=false): не пересобирается при генерации,
	// но её страница остаётся доступной. В активных списках не показывается.
	Archived bool
}

type AdminGroupAccountsPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	Sites      []AdminSiteAccounts
}

type AdminSiteAccounts struct {
	Site         string
	Count        int
	AccountsText string // account_id по одному в строке, готово к копированию
}

type AdminGroupGradesPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	Columns    []AdminManualGradeColumn
	Rows       []AdminManualGradeRow
	// ConfigJSON — текущая конфигурация оценок группы (grades из group.json)
	// для формы-конструктора столбцов. TableNames — вкладки группы для подсказок.
	ConfigJSON template.JS
	TableNames []string
	// APIBase — префикс эндпоинтов сохранения: "/api/admin" в админке,
	// "/api/group-panel" при входе доступом группы. RoleTitle — подпись доступа
	// в шапке (пусто в админке). BackURL/BackLabel — ссылка «назад».
	APIBase   string
	RoleTitle string
	BackURL   string
	BackLabel string
	// GroupToken — токен доступа для ссылок на страницы группы.
	GroupToken string
	// CanEditConfig/CanEditGrades — какие блоки страницы доступны (в админке —
	// оба, у доступа группы — по правам grades.config / grades.manual).
	CanEditConfig bool
	CanEditGrades bool
}

type AdminStudentProfilePageData struct {
	PageTitle    string
	Footer       FooterInfo
	StudentID    string
	NotGenerated bool
	Profile      domain.GeneratedStudentProfile
	MaxDaily     int
	// BackURL/BackLabel — ссылка «назад» (в админку или к участникам по токену).
	BackURL   string
	BackLabel string
	// TokenView — просмотр по токену группы (не админ): без админ-ссылок на группы.
	TokenView bool
	// Token — токен группы для ссылок со страницы (пуст в админ-режиме).
	Token string
	// CanReviewFlags — можно размечать флаги нечестности (админка или право
	// flags.review у доступа группы).
	CanReviewFlags bool
	// HasGlobalCourse — хотя бы у одного курса есть глобальный вариант темпа
	// (когорта шире группы): показываем тумблер «эта группа / все группы».
	HasGlobalCourse bool
}

// AdminStudentProfilePage — админский профиль участника: активность, аналитика
// решений, позиции в группах (из generated/students/<id>.json).
func (h *Handlers) AdminStudentProfilePage(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !domain.IsValidSlug(id) {
		http.NotFound(w, r)
		return
	}

	page := AdminStudentProfilePageData{
		Footer:         h.buildFooterInfo(),
		StudentID:      id,
		BackURL:        "/standings/admin/students",
		BackLabel:      "← Ученики",
		CanReviewFlags: true,
	}
	h.fillStudentProfilePage(&page, id, nil)
	if err := h.renderer.Render(w, http.StatusOK, "admin_student.html", page); err != nil {
		h.logger.Printf("ERROR render admin student profile id=%s err=%v", id, err)
	}
}

// fillStudentProfilePage загружает профиль в данные страницы. onlyGroup!=nil —
// оставить в «Позициях в группах» только эту группу (просмотр по токену).
func (h *Handlers) fillStudentProfilePage(page *AdminStudentProfilePageData, id string, onlyGroup *string) {
	page.PageTitle = "Профиль — " + id
	profile, err := h.loader.LoadStudentProfile(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			page.NotGenerated = true
		} else {
			h.logger.Printf("ERROR student profile id=%s err=%v", id, err)
			page.NotGenerated = true
		}
		return
	}
	if onlyGroup != nil {
		filtered := profile.Groups[:0:0]
		for _, g := range profile.Groups {
			if g.Slug == *onlyGroup {
				filtered = append(filtered, g)
			}
		}
		profile.Groups = filtered
		// Темп курса — тоже только по этой группе (жюри не видит чужие курсы).
		courses := profile.CourseStats[:0:0]
		for _, cs := range profile.CourseStats {
			if cs.GroupSlug == *onlyGroup {
				courses = append(courses, cs)
			}
		}
		profile.CourseStats = courses
	}
	applyFlagReviews(h.loadFlagReviewIndex(), id, profile.CourseStats)
	// Профиль открыт только персоналу — ejudge в режиме судьи.
	domain.SwapEjudgeLinksInCourseStats(profile.CourseStats)
	for i := range profile.CourseStats {
		if profile.CourseStats[i].Global != nil {
			page.HasGlobalCourse = true
			break
		}
	}
	page.Profile = profile
	if profile.PublicName != "" {
		page.PageTitle = "Профиль — " + profile.PublicName
	}
	for _, d := range profile.DailyActivity {
		if d.Count > page.MaxDaily {
			page.MaxDaily = d.Count
		}
	}
}

type AdminManualGradeColumn struct {
	ID    string
	Title string
}

type AdminManualGradeRow struct {
	StudentID  string
	PublicName string
	Values     []string // по одному значению на ручной столбец; "" — нет оценки
}

type adminGroupGradesSaveRequest struct {
	Slug   string                        `json:"slug"`
	Grades map[string]map[string]float64 `json:"grades"`
}

type adminFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type adminIntakeMergeRequest struct {
	Content string `json:"content"`
}

type adminCommand struct {
	Path string
	Args []string
}

func (h *Handlers) ConfigureAdmin(cfg AdminConfig) error {
	cfg.Login = strings.TrimSpace(cfg.Login)
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.ProjectRoot = strings.TrimSpace(cfg.ProjectRoot)
	cfg.DataDir = strings.TrimSpace(cfg.DataDir)
	cfg.GeneratedDir = strings.TrimSpace(cfg.GeneratedDir)

	if cfg.Login == "" || cfg.Password == "" {
		return fmt.Errorf("admin credentials are required")
	}
	if cfg.ProjectRoot == "" {
		return fmt.Errorf("project root is required")
	}

	projectRoot, err := filepath.Abs(cfg.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	cfg.ProjectRoot = projectRoot

	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(cfg.ProjectRoot, "data")
	} else if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(cfg.ProjectRoot, cfg.DataDir)
	}
	cfg.DataDir = filepath.Clean(cfg.DataDir)

	if cfg.GeneratedDir == "" {
		cfg.GeneratedDir = filepath.Join(cfg.ProjectRoot, "generated")
	} else if !filepath.IsAbs(cfg.GeneratedDir) {
		cfg.GeneratedDir = filepath.Join(cfg.ProjectRoot, cfg.GeneratedDir)
	}
	cfg.GeneratedDir = filepath.Clean(cfg.GeneratedDir)

	h.admin = &adminState{cfg: cfg}
	return nil
}

func (h *Handlers) AdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.admin == nil {
			http.Error(w, "admin is not configured", http.StatusInternalServerError)
			return
		}
		user, pass, ok := r.BasicAuth()
		userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(h.admin.cfg.Login)) == 1
		passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(h.admin.cfg.Password)) == 1
		if !ok || !userMatch || !passMatch {
			w.Header().Set("WWW-Authenticate", `Basic realm="admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *Handlers) AdminPage(w http.ResponseWriter, _ *http.Request) {
	files, err := h.listEditableFiles()
	if err != nil {
		h.logger.Printf("ERROR list editable files: %v", err)
		files = []string{"data/students.json", "data/contests.json", adminIntakePath}
	}
	groupLinks, err := h.listAdminGroupLinks()
	if err != nil {
		h.logger.Printf("ERROR list admin groups: %v", err)
		groupLinks = nil
	}
	combinedGroups, selectableGroups := h.listCombinedGroups()

	defaultPath := ""
	if len(files) > 0 {
		defaultPath = files[0]
	}

	page := AdminPageData{
		PageTitle:        "Admin",
		Footer:           h.buildFooterInfo(),
		Editable:         files,
		Groups:           groupLinks,
		CombinedGroups:   combinedGroups,
		SelectableGroups: selectableGroups,
		LastResult:       h.lastAdminResult(),
		DefaultPath:      defaultPath,
		Accesses:         h.buildAccessEditor(true, "", "/api/admin/global-accesses/save", h.loadGlobalAccesses()),
	}
	for _, g := range groupLinks {
		if g.IsCombined {
			continue
		}
		if g.Archived {
			page.ArchivedCount++
		} else {
			page.HasActiveGroups = true
		}
	}
	page.HasArchivedGroups = page.ArchivedCount > 0
	if err := h.renderer.Render(w, http.StatusOK, "admin.html", page); err != nil {
		h.logger.Printf("ERROR render admin page: %v", err)
	}
}

func (h *Handlers) AdminActionGenerate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	// refresh_tasks=1 — перечитать состав задач (оглавления сборников, названия)
	// с сайтов, минуя дисковый кэш; без него генерация быстрая, из кэша.
	refreshTasks := r.FormValue("refresh_tasks") == "1"
	result := h.runAdminAction("generate", func() AdminActionResult {
		return h.executeGenerateAction(refreshTasks)
	})
	h.setAdminResult(result)
	http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
}

// AdminActionResetCache сбрасывает кеш informatics/codeforces. Параметры формы:
// period (week|month|year|all) — за какой срок сбросить (для week/month/year
// трогаются только аккаунты с активностью за этот период); scope (all|student|
// group) и target (id ученика / slug группы) — чьи аккаунты. Сброшенные аккаунты
// перечитаются с нуля при ближайшей генерации.
func (h *Handlers) AdminActionResetCache(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	period := strings.TrimSpace(r.FormValue("period"))
	scope := strings.TrimSpace(r.FormValue("scope"))
	target := strings.TrimSpace(r.FormValue("target"))
	result := h.runAdminAction("reset_cache", func() AdminActionResult {
		return h.executeResetCacheAction(period, scope, target)
	})
	h.setAdminResult(result)
	http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
}

var cacheResetPeriodLabels = map[string]string{
	"week":  "неделя",
	"month": "месяц",
	"year":  "год",
	"all":   "всё время",
}

func (h *Handlers) executeResetCacheAction(period, scope, target string) AdminActionResult {
	started := time.Now()

	if period == "" {
		period = "all"
	}
	var since time.Time
	switch period {
	case "week":
		since = time.Now().AddDate(0, 0, -7)
	case "month":
		since = time.Now().AddDate(0, -1, 0)
	case "year":
		since = time.Now().AddDate(-1, 0, 0)
	case "all":
		since = time.Time{}
	default:
		return newAdminResult("reset_cache", false, -1, started, "", []string{"неизвестный период: " + period})
	}

	// nil-списки означают «все аккаунты»; для ученика/группы — только их аккаунты.
	var infAccounts, cfAccounts []string
	var scopeDesc string
	if scope == "" {
		scope = "all"
	}
	switch scope {
	case "all":
		scopeDesc = "все аккаунты"
	case "student":
		if target == "" {
			return newAdminResult("reset_cache", false, -1, started, "", []string{"укажите id ученика"})
		}
		inf, cf, err := h.collectSiteAccounts(map[string]struct{}{target: {}})
		if err != nil {
			return newAdminResult("reset_cache", false, -1, started, "", []string{err.Error()})
		}
		infAccounts, cfAccounts = inf, cf
		scopeDesc = "ученик " + target
	case "group":
		if target == "" {
			return newAdminResult("reset_cache", false, -1, started, "", []string{"укажите slug группы"})
		}
		gf, ok, err := h.readGroupFile(target)
		if err != nil {
			return newAdminResult("reset_cache", false, -1, started, "", []string{err.Error()})
		}
		if !ok {
			return newAdminResult("reset_cache", false, -1, started, "", []string{"группа не найдена: " + target})
		}
		ids := make(map[string]struct{}, len(gf.StudentIDs))
		for _, id := range gf.StudentIDs {
			ids[id] = struct{}{}
		}
		inf, cf, err := h.collectSiteAccounts(ids)
		if err != nil {
			return newAdminResult("reset_cache", false, -1, started, "", []string{err.Error()})
		}
		infAccounts, cfAccounts = inf, cf
		scopeDesc = "группа " + target
	default:
		return newAdminResult("reset_cache", false, -1, started, "", []string{"неизвестная цель: " + scope})
	}

	infPath := filepath.Join(h.admin.cfg.GeneratedDir, "cache", "informatics_runs_state.json")
	cfPath := filepath.Join(h.admin.cfg.GeneratedDir, "cache", "codeforces_user_status_state.json")

	var output bytes.Buffer
	var errorsList []string
	infN, err := source.ClearInformaticsCache(infPath, infAccounts, since)
	if err != nil {
		errorsList = append(errorsList, "informatics: "+err.Error())
	}
	cfN, err := source.ClearCodeforcesCache(cfPath, cfAccounts, since)
	if err != nil {
		errorsList = append(errorsList, "codeforces: "+err.Error())
	}

	fmt.Fprintf(&output, "сброс кеша (%s, период: %s): informatics — %d аккаунтов, codeforces — %d аккаунтов",
		scopeDesc, cacheResetPeriodLabels[period], infN, cfN)

	return newAdminResult("reset_cache", len(errorsList) == 0, 0, started, output.String(), errorsList)
}

// collectSiteAccounts возвращает account_id учеников (из фильтра studentIDs;
// nil — все) по сайтам informatics и codeforces. Пустые срезы (не nil) означают
// «у выбранных учеников таких аккаунтов нет».
func (h *Handlers) collectSiteAccounts(studentIDs map[string]struct{}) (informatics, codeforces []string, err error) {
	students, err := studentintake.LoadStudentsFile(h.dataPath("students.json"))
	if err != nil {
		return nil, nil, err
	}
	informatics = []string{}
	codeforces = []string{}
	for _, s := range students {
		if studentIDs != nil {
			if _, ok := studentIDs[s.ID]; !ok {
				continue
			}
		}
		for _, a := range s.Accounts {
			id := strings.TrimSpace(a.AccountID)
			if id == "" {
				continue
			}
			switch domain.NormalizeSite(a.Site) {
			case "informatics":
				informatics = append(informatics, id)
			case "codeforces":
				codeforces = append(codeforces, id)
			}
		}
	}
	return informatics, codeforces, nil
}

func (h *Handlers) AdminGroupCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		result := newAdminResult(
			"create_group",
			false,
			-1,
			time.Now(),
			"",
			[]string{fmt.Sprintf("parse form: %v", err)},
		)
		h.setAdminResult(result)
		http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
		return
	}

	slug := strings.TrimSpace(r.FormValue("slug"))
	name := strings.TrimSpace(r.FormValue("name"))
	formLink := strings.TrimSpace(r.FormValue("form_link"))
	shortName := strings.TrimSpace(r.FormValue("short_name"))

	result := h.runAdminAction("create_group", func() AdminActionResult {
		return h.executeCreateGroupAction(slug, name, formLink, shortName)
	})
	h.setAdminResult(result)
	http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
}

func (h *Handlers) AdminFiles(w http.ResponseWriter, _ *http.Request) {
	files, err := h.listEditableFiles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"files": files,
	})
}

func (h *Handlers) AdminFile(w http.ResponseWriter, r *http.Request) {
	logicalPath := strings.TrimSpace(r.URL.Query().Get("path"))
	normalizedPath, absPath, err := h.resolveEditablePath(logicalPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	body, err := os.ReadFile(absPath)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    normalizedPath,
		"content": string(body),
	})
}

func (h *Handlers) AdminFileValidate(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAdminFileRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if _, _, err := h.resolveEditablePath(req.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if err := validateJSONSyntax(req.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    req.Path,
		"message": "JSON is valid",
	})
}

func (h *Handlers) AdminFileSave(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAdminFileRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	normalizedPath, absPath, err := h.resolveEditablePath(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	// Ссылки informatics в сыром JSON приводим к настроенному зеркалу.
	req.Content = h.rewriteInformaticsText(req.Content)
	if err := validateJSONSyntax(req.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	mode, err := fileutil.DetectFileMode(absPath, 0o644)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	if err := fileutil.WriteFileAtomic(absPath, []byte(req.Content), mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    normalizedPath,
		"message": "saved",
	})
}

// AdminIntakePage — страница «Анкеты учеников»: форма над тем же staging-флоу
// (prepare → dry-run → merge), что и раньше; данные подтягивает JS.
func (h *Handlers) AdminIntakePage(w http.ResponseWriter, _ *http.Request) {
	page := struct {
		PageTitle string
		Footer    FooterInfo
	}{PageTitle: "Анкеты учеников", Footer: h.buildFooterInfo()}
	if err := h.renderer.Render(w, http.StatusOK, "admin_intake.html", page); err != nil {
		h.logger.Printf("ERROR render admin intake page: %v", err)
	}
}

func (h *Handlers) AdminIntakeStagingPrepare(w http.ResponseWriter, _ *http.Request) {
	if h.intake == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": "intake store is not configured",
		})
		return
	}

	stagingPath := filepath.Join(h.admin.cfg.DataDir, "student_intake_admin.json")
	body, err := h.intake.PrepareAdminIntakeStaging(stagingPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    adminIntakeStagingPath,
		"content": string(body),
	})
}

func (h *Handlers) AdminIntakeStagingMerge(w http.ResponseWriter, r *http.Request) {
	if h.intake == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": "intake store is not configured",
		})
		return
	}

	req, err := decodeAdminIntakeMergeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if err := validateJSONSyntax(req.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	stagingPath := filepath.Join(h.admin.cfg.DataDir, "student_intake_admin.json")
	if err := h.intake.SaveAdminIntakeStaging(stagingPath, []byte(req.Content)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	result := h.runAdminAction("merge_intake_staging", func() AdminActionResult {
		return h.executeMergeIntakeStagingAction(stagingPath)
	})
	h.setAdminResult(result)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"action_success": result.Success,
	})
}

// AdminIntakeMergeDryRun — пробный merge intake из содержимого редактора:
// показывает, какие анкеты в кого разрешатся и в какие группы попадут, без
// записи на диск.
func (h *Handlers) AdminIntakeMergeDryRun(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	req, err := decodeAdminIntakeMergeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	intake, err := studentintake.ParseIntakeBytes([]byte(req.Content))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	existing, err := studentintake.LoadStudentsFile(filepath.Join(h.admin.cfg.DataDir, "students.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	preview, err := studentintake.BuildMergePreview(h.admin.cfg.DataDir, existing, intake)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preview": preview})
}

func (h *Handlers) runAdminAction(action string, runner func() AdminActionResult) AdminActionResult {
	started := time.Now()
	if h.admin == nil {
		return newAdminResult(action, false, -1, started, "", []string{"admin is not configured"})
	}
	if !h.admin.actionMu.TryLock() {
		return newAdminResult(action, false, -1, started, "", []string{"another admin action is already running"})
	}
	defer h.admin.actionMu.Unlock()
	return runner()
}

func (h *Handlers) executeGenerateAction(refreshTasks bool) AdminActionResult {
	generateBinary := filepath.Join(h.admin.cfg.ProjectRoot, "bin", "generate")
	args := []string{
		"-data-dir", h.admin.cfg.DataDir,
		"-generated-dir", h.admin.cfg.GeneratedDir,
		"-informatics-creds-file", filepath.Join(h.admin.cfg.DataDir, "credentials", "informatics_credentials.json"),
		"-codeforces-creds-file", filepath.Join(h.admin.cfg.DataDir, "credentials", "codeforces_credentials.json"),
	}
	if refreshTasks {
		args = append(args, "-refresh-tasks")
	}
	commands := []adminCommand{{Path: generateBinary, Args: args}}
	return h.runCommandSequence("generate", commands)
}

func (h *Handlers) executeCreateGroupAction(slug, name, formLink, shortName string) AdminActionResult {
	createGroupBinary := filepath.Join(h.admin.cfg.ProjectRoot, "bin", "create_group")
	args := []string{
		"-data-dir", h.admin.cfg.DataDir,
		"-slug", slug,
		"-name", name,
		"-form-link", formLink,
	}
	if shortName != "" {
		args = append(args, "-short-name", shortName)
	}
	commands := []adminCommand{{Path: createGroupBinary, Args: args}}
	return h.runCommandSequence("create_group", commands)
}

func (h *Handlers) executeMergeIntakeStagingAction(stagingPath string) AdminActionResult {
	mergeBinary := filepath.Join(h.admin.cfg.ProjectRoot, "bin", "merge_students")
	commands := []adminCommand{
		{
			Path: mergeBinary,
			Args: []string{
				"-data-dir", h.admin.cfg.DataDir,
				"-intake-file", stagingPath,
				"-write",
			},
		},
	}
	return h.runCommandSequence("merge_intake_staging", commands)
}

func (h *Handlers) runCommandSequence(action string, commands []adminCommand) AdminActionResult {
	started := time.Now()
	if len(commands) == 0 {
		return newAdminResult(action, false, -1, started, "", []string{"no commands configured"})
	}

	var output bytes.Buffer
	exitCode := 0
	errorsList := make([]string, 0)

	for idx, command := range commands {
		if idx > 0 {
			output.WriteString("\n")
		}
		output.WriteString("$ ")
		output.WriteString(renderCommand(command.Path, command.Args))
		output.WriteString("\n")

		cmd := exec.Command(command.Path, command.Args...)
		cmd.Dir = h.admin.cfg.ProjectRoot
		cmd.Stdout = &output
		cmd.Stderr = &output

		err := cmd.Run()
		if err != nil {
			exitCode = commandExitCode(err)
			errorsList = append(errorsList, fmt.Sprintf("command failed: %s (exit code %d)", renderCommand(command.Path, command.Args), exitCode))
			if exitCode < 0 {
				errorsList = append(errorsList, err.Error())
			}
			break
		}
	}

	success := len(errorsList) == 0
	return newAdminResult(action, success, exitCode, started, output.String(), errorsList)
}

func (h *Handlers) lastAdminResult() *AdminActionResult {
	if h.admin == nil {
		return nil
	}
	h.admin.resultMu.RLock()
	defer h.admin.resultMu.RUnlock()
	if h.admin.lastResult == nil {
		return nil
	}
	resultCopy := *h.admin.lastResult
	if len(resultCopy.Errors) > 0 {
		resultCopy.Errors = append([]string(nil), resultCopy.Errors...)
	}
	return &resultCopy
}

func (h *Handlers) setAdminResult(result AdminActionResult) {
	if h.admin == nil {
		return
	}
	h.admin.resultMu.Lock()
	defer h.admin.resultMu.Unlock()
	resultCopy := result
	if len(resultCopy.Errors) > 0 {
		resultCopy.Errors = append([]string(nil), resultCopy.Errors...)
	}
	h.admin.lastResult = &resultCopy
}

func (h *Handlers) listEditableFiles() ([]string, error) {
	if h.admin == nil {
		return nil, fmt.Errorf("admin is not configured")
	}

	files := []string{"data/students.json", "data/contests.json", adminIntakePath}
	groupsDir := filepath.Join(h.admin.cfg.DataDir, "groups")
	entries, err := os.ReadDir(groupsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return files, nil
		}
		return nil, fmt.Errorf("read groups dir: %w", err)
	}

	slugs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := strings.TrimSpace(entry.Name())
		if !domain.IsValidSlug(slug) {
			continue
		}
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		files = append(files,
			filepath.ToSlash(filepath.Join("data", "groups", slug, "group.json")),
			filepath.ToSlash(filepath.Join("data", "groups", slug, "contests.json")),
		)
	}

	return files, nil
}

func (h *Handlers) listAdminGroupLinks() ([]AdminGroupLink, error) {
	if h.admin == nil {
		return nil, fmt.Errorf("admin is not configured")
	}

	groupsDir := filepath.Join(h.admin.cfg.DataDir, "groups")
	entries, err := os.ReadDir(groupsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read groups dir: %w", err)
	}

	out := make([]AdminGroupLink, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := strings.TrimSpace(entry.Name())
		if !domain.IsValidSlug(slug) {
			continue
		}

		title := slug
		var members []string
		archived := false
		groupPath := filepath.Join(groupsDir, slug, "group.json")
		body, readErr := os.ReadFile(groupPath)
		if readErr == nil {
			var groupFile domain.GroupFile
			if unmarshalErr := json.Unmarshal(body, &groupFile); unmarshalErr == nil {
				if groupTitle := strings.TrimSpace(groupFile.Title); groupTitle != "" {
					title = groupTitle
				}
				members = groupFile.MemberGroups
				archived = groupFile.Update != nil && !*groupFile.Update
			}
		}

		out = append(out, AdminGroupLink{
			Slug:       slug,
			Title:      title,
			URL:        "/standings/" + slug,
			IsCombined: len(members) > 0,
			Members:    members,
			Archived:   archived,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

func (h *Handlers) AdminGroupAccounts(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}

	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if !domain.IsValidSlug(slug) {
		http.NotFound(w, r)
		return
	}

	groupPath := filepath.Join(h.admin.cfg.DataDir, "groups", slug, "group.json")
	groupBody, err := os.ReadFile(groupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.logger.Printf("ERROR admin group accounts read group slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var groupFile domain.GroupFile
	if err := json.Unmarshal(groupBody, &groupFile); err != nil {
		h.logger.Printf("ERROR admin group accounts decode group slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	title := strings.TrimSpace(groupFile.Title)
	if title == "" {
		title = slug
	}

	var students []domain.Student
	if err := fileutil.ReadJSON(filepath.Join(h.admin.cfg.DataDir, "students.json"), &students); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			h.logger.Printf("ERROR admin group accounts read students err=%v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		students = nil
	}
	byID := make(map[string]domain.Student, len(students))
	for _, student := range domain.NormalizeStudents(students) {
		if student.ID != "" {
			byID[student.ID] = student
		}
	}

	sites := collectGroupSiteAccounts(groupFile.StudentIDs, byID)

	page := AdminGroupAccountsPageData{
		PageTitle:  "Аккаунты — " + title,
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		Sites:      sites,
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_group_accounts.html", page); err != nil {
		h.logger.Printf("ERROR render admin group accounts slug=%s err=%v", slug, err)
	}
}

// collectGroupSiteAccounts собирает account_id учеников группы по сайтам,
// сохраняя порядок студентов и убирая дубли внутри сайта.
func collectGroupSiteAccounts(studentIDs []string, byID map[string]domain.Student) []AdminSiteAccounts {
	perSite := make(map[string][]string)
	seenPerSite := make(map[string]map[string]struct{})
	siteOrder := make([]string, 0)

	for _, studentID := range studentIDs {
		student, ok := byID[strings.TrimSpace(studentID)]
		if !ok {
			continue
		}
		for _, account := range student.Accounts {
			site := account.Site
			accountID := account.AccountID
			if site == "" || accountID == "" {
				continue
			}
			if _, ok := perSite[site]; !ok {
				siteOrder = append(siteOrder, site)
				seenPerSite[site] = make(map[string]struct{})
			}
			if _, dup := seenPerSite[site][accountID]; dup {
				continue
			}
			seenPerSite[site][accountID] = struct{}{}
			perSite[site] = append(perSite[site], accountID)
		}
	}

	sort.Strings(siteOrder)
	out := make([]AdminSiteAccounts, 0, len(siteOrder))
	for _, site := range siteOrder {
		ids := perSite[site]
		out = append(out, AdminSiteAccounts{
			Site:         site,
			Count:        len(ids),
			AccountsText: strings.Join(ids, "\n"),
		})
	}
	return out
}

func (h *Handlers) AdminGroupGradesPage(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	page, ok, err := h.buildGroupGradesData(slug)
	if err != nil {
		h.logger.Printf("ERROR admin group grades slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	page.APIBase = "/api/admin"
	page.BackURL = "/standings/admin/group?slug=" + slug
	page.BackLabel = "Управление группой"
	page.CanEditConfig, page.CanEditGrades = true, true
	if err := h.renderer.Render(w, http.StatusOK, "admin_group_grades.html", page); err != nil {
		h.logger.Printf("ERROR render admin group grades slug=%s err=%v", slug, err)
	}
}

// buildGroupGradesData собирает страницу оценок группы: конструктор столбцов
// (веса, метрика, нормировка, коэффициенты) и сетка ручных оценок. Общая для
// админки и панели группы (роль «Жюри»). ok=false — группы нет.
func (h *Handlers) buildGroupGradesData(slug string) (AdminGroupGradesPageData, bool, error) {
	var empty AdminGroupGradesPageData
	if !domain.IsValidSlug(slug) {
		return empty, false, nil
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil {
		return empty, false, err
	}
	if !ok {
		return empty, false, nil
	}

	title := strings.TrimSpace(groupFile.Title)
	if title == "" {
		title = slug
	}
	manualColumns := manualGradeColumns(groupFile)
	columns := make([]AdminManualGradeColumn, 0, len(manualColumns))
	for _, col := range manualColumns {
		colTitle := strings.TrimSpace(col.Title)
		if colTitle == "" {
			colTitle = col.ID
		}
		columns = append(columns, AdminManualGradeColumn{ID: col.ID, Title: colTitle})
	}

	publicNames := h.loadPublicNames()
	manual, err := h.loadManualGrades(slug)
	if err != nil {
		return empty, false, err
	}

	rows := make([]AdminManualGradeRow, 0, len(groupFile.StudentIDs))
	for _, studentID := range domain.NormalizeGroups(groupFile.StudentIDs) {
		publicName := publicNames[studentID]
		if publicName == "" {
			publicName = studentID
		}
		values := make([]string, len(manualColumns))
		for i, col := range manualColumns {
			if byStudent, ok := manual[col.ID]; ok {
				if v, ok := byStudent[studentID]; ok {
					values[i] = strconv.FormatFloat(v, 'f', -1, 64)
				}
			}
		}
		rows = append(rows, AdminManualGradeRow{StudentID: studentID, PublicName: publicName, Values: values})
	}
	sortManualGradeRows(rows)

	// Конфиг оценок как есть (или пустой каркас) — для формы-конструктора столбцов.
	cfg := groupFile.Grades
	if cfg == nil {
		cfg = &domain.GradesConfig{Columns: []domain.GradeColumn{}}
	}
	cfgBlob, err := json.Marshal(cfg)
	if err != nil {
		return empty, false, err
	}

	page := AdminGroupGradesPageData{
		PageTitle:  "Оценки — " + title,
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		Columns:    columns,
		Rows:       rows,
		ConfigJSON: template.JS(cfgBlob),
		TableNames: h.groupTableNames(slug),
	}
	return page, true, nil
}

// groupTableNames собирает различающиеся вкладки (table_name) контестов группы —
// подсказки для поля столбца оценки типа table. Best-effort: учитывает
// переопределения ссылок и inline-определения.
func (h *Handlers) groupTableNames(slug string) []string {
	entries, err := h.loadGroupContestEntries(slug)
	if err != nil {
		return nil
	}
	globals, _ := h.loadContestsList()
	byID := make(map[string]domain.Contest, len(globals))
	for _, c := range globals {
		byID[strings.TrimSpace(c.ID)] = c
	}

	seen := make(map[string]struct{})
	out := make([]string, 0)
	add := func(names domain.TableNameList) {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	for _, e := range entries {
		switch {
		case e.inline:
			var c domain.Contest
			if json.Unmarshal(e.raw, &c) == nil {
				add(c.TableNames)
			}
		case e.tableName != "":
			add(domain.NormalizeTableNames(parseTableNameField(e.tableName)))
		default:
			if c, ok := byID[e.id]; ok {
				add(c.TableNames)
			}
		}
	}
	return out
}

// sortManualGradeRows сортирует строки таблиц ручного заполнения по имени
// ученика (без учёта регистра, ё=е) — так удобнее искать при вводе. Порядок
// в самих таблицах standings не меняется.
func sortManualGradeRows(rows []AdminManualGradeRow) {
	norm := func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "ё", "е")
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return norm(rows[i].PublicName) < norm(rows[j].PublicName)
	})
}

func (h *Handlers) AdminGroupGradesSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminJSONBodyBytes))
	var req adminGroupGradesSaveRequest
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !domain.IsValidSlug(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid slug"})
		return
	}

	if status, msg := h.saveManualGrades(slug, req.Grades); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// saveManualGrades валидирует и пишет grades_manual.json группы: только
// известные ручные столбцы и ученики группы, оценки зажимаются в диапазон.
// Общий код админки и жюри-панели (по токену группы).
func (h *Handlers) saveManualGrades(slug string, grades map[string]map[string]float64) (int, string) {
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		return http.StatusBadRequest, "group not found"
	}

	allowedColumns := make(map[string]struct{})
	for _, col := range manualGradeColumns(groupFile) {
		allowedColumns[col.ID] = struct{}{}
	}
	allowedStudents := make(map[string]struct{})
	for _, studentID := range domain.NormalizeGroups(groupFile.StudentIDs) {
		allowedStudents[studentID] = struct{}{}
	}

	out := make(map[string]map[string]float64)
	for colID, byStudent := range grades {
		if _, ok := allowedColumns[colID]; !ok {
			continue
		}
		clean := make(map[string]float64)
		for studentID, value := range byStudent {
			if _, ok := allowedStudents[studentID]; !ok {
				continue
			}
			clean[studentID] = clampGrade(value)
		}
		if len(clean) > 0 {
			out[colID] = clean
		}
	}

	path := filepath.Join(h.admin.cfg.DataDir, "groups", slug, "grades_manual.json")
	if err := fileutil.WriteJSON(path, out, 0o644); err != nil {
		h.logger.Printf("ERROR group grades save slug=%s err=%v", slug, err)
		return http.StatusInternalServerError, err.Error()
	}
	return http.StatusOK, ""
}

type adminGradesConfigColumn struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Weight    float64         `json:"weight"`
	Type      string          `json:"type"`
	TableName string          `json:"table_name"`
	Metric    string          `json:"metric"`
	Normalize json.RawMessage `json:"normalize"`
	Upsolving *float64        `json:"upsolving"`
	// IgnoreMissing — не учитывать целиком пропущенные контесты (см.
	// domain.GradeColumn.IgnoreMissingContests). Только для type=table.
	IgnoreMissing bool `json:"ignore_missing_contests"`
}

type adminGradesConfigSaveRequest struct {
	Slug    string                    `json:"slug"`
	Title   string                    `json:"title"`
	Round   *int                      `json:"round"`
	Columns []adminGradesConfigColumn `json:"columns"`
}

// AdminGroupGradesConfigSave сохраняет конфигурацию столбцов оценок (grades в
// group.json) из формы-конструктора, не трогая остальные поля группы.
func (h *Handlers) AdminGroupGradesConfigSave(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminJSONBodyBytes))
	var req adminGradesConfigSaveRequest
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	if status, msg := h.saveGradesConfig(strings.TrimSpace(req.Slug), req); msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// saveGradesConfig записывает конфигурацию оценок группы (заголовок, округление,
// столбцы с весами и параметрами). Общий код админки и панели (роль «Жюри»).
func (h *Handlers) saveGradesConfig(slug string, req adminGradesConfigSaveRequest) (int, string) {
	if !domain.IsValidSlug(slug) {
		return http.StatusBadRequest, "invalid slug"
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		return http.StatusBadRequest, "group not found"
	}
	columns, err := buildGradeColumns(req.Columns)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if len(columns) == 0 {
		// Нет столбцов — оценок у группы нет, убираем секцию целиком.
		groupFile.Grades = nil
	} else {
		groupFile.Grades = &domain.GradesConfig{
			Title:   strings.TrimSpace(req.Title),
			Round:   req.Round,
			Columns: columns,
		}
	}
	if err := h.writeGroupFile(slug, groupFile); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return http.StatusOK, ""
}

// buildGradeColumns валидирует и собирает столбцы оценок из формы.
func buildGradeColumns(in []adminGradesConfigColumn) ([]domain.GradeColumn, error) {
	out := make([]domain.GradeColumn, 0, len(in))
	seenID := make(map[string]struct{}, len(in))
	for i, c := range in {
		title := domain.NormalizeWhitespace(c.Title)
		if title == "" {
			return nil, fmt.Errorf("столбец #%d: укажите название", i+1)
		}
		typ := strings.ToLower(strings.TrimSpace(c.Type))
		if typ != domain.GradeColumnTable && typ != domain.GradeColumnManual {
			return nil, fmt.Errorf("столбец %q: тип должен быть «table» или «manual»", title)
		}
		id := strings.TrimSpace(c.ID)
		if id == "" {
			id = fmt.Sprintf("col%d", i+1)
		}
		if _, dup := seenID[id]; dup {
			return nil, fmt.Errorf("столбец %q: id %q уже используется", title, id)
		}
		seenID[id] = struct{}{}
		if c.Weight < 0 {
			return nil, fmt.Errorf("столбец %q: вес не может быть отрицательным", title)
		}

		col := domain.GradeColumn{
			ID:     id,
			Title:  title,
			Weight: c.Weight,
			Type:   typ,
		}
		if typ == domain.GradeColumnTable {
			col.TableName = strings.TrimSpace(c.TableName)
			metric := strings.ToLower(strings.TrimSpace(c.Metric))
			if metric != "" && metric != domain.GradeMetricPlus && metric != domain.GradeMetricScore {
				return nil, fmt.Errorf("столбец %q: метрика должна быть «plus» или «score»", title)
			}
			col.Metric = metric

			if len(c.Normalize) > 0 && string(c.Normalize) != "null" {
				if err := json.Unmarshal(c.Normalize, &col.Normalize); err != nil {
					return nil, fmt.Errorf("столбец %q: normalize — %v", title, err)
				}
			} else {
				col.Normalize = domain.NormalizeSpec{Mode: domain.NormalizeMax}
			}
			if c.Upsolving != nil {
				u := *c.Upsolving
				if u < 0 || u > 1 {
					return nil, fmt.Errorf("столбец %q: коэффициент дорешки должен быть от 0 до 1", title)
				}
				col.Upsolving = &u
			}
			col.IgnoreMissingContests = c.IgnoreMissing
		}
		out = append(out, col)
	}
	return out, nil
}

func (h *Handlers) readGroupFile(slug string) (domain.GroupFile, bool, error) {
	path := filepath.Join(h.admin.cfg.DataDir, "groups", slug, "group.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.GroupFile{}, false, nil
		}
		return domain.GroupFile{}, false, err
	}
	var groupFile domain.GroupFile
	if err := json.Unmarshal(body, &groupFile); err != nil {
		return domain.GroupFile{}, false, err
	}
	return groupFile, true, nil
}

func (h *Handlers) loadPublicNames() map[string]string {
	out := make(map[string]string)
	for id, s := range h.loadStudentsByID() {
		out[id] = s.PublicName
	}
	return out
}

// loadStudentsByID читает students.json в карту по id (пустая при ошибке).
func (h *Handlers) loadStudentsByID() map[string]domain.Student {
	var students []domain.Student
	if err := fileutil.ReadJSON(filepath.Join(h.admin.cfg.DataDir, "students.json"), &students); err != nil {
		return map[string]domain.Student{}
	}
	out := make(map[string]domain.Student, len(students))
	for _, student := range domain.NormalizeStudents(students) {
		if student.ID != "" {
			out[student.ID] = student
		}
	}
	return out
}

func (h *Handlers) loadManualGrades(slug string) (map[string]map[string]float64, error) {
	body, err := os.ReadFile(filepath.Join(h.admin.cfg.DataDir, "groups", slug, "grades_manual.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]map[string]float64{}, nil
		}
		return nil, err
	}
	var out map[string]map[string]float64
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]map[string]float64{}
	}
	return out, nil
}

func manualGradeColumns(groupFile domain.GroupFile) []domain.GradeColumn {
	if groupFile.Grades == nil {
		return nil
	}
	out := make([]domain.GradeColumn, 0, len(groupFile.Grades.Columns))
	for _, col := range groupFile.Grades.Columns {
		if strings.EqualFold(col.Type, domain.GradeColumnManual) && strings.TrimSpace(col.ID) != "" {
			out = append(out, col)
		}
	}
	return out
}

func clampGrade(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 10 {
		return 10
	}
	return v
}

func (h *Handlers) resolveEditablePath(path string) (string, string, error) {
	if h.admin == nil {
		return "", "", fmt.Errorf("admin is not configured")
	}

	path = strings.TrimSpace(path)
	path = strings.ReplaceAll(path, "\\", "/")

	switch path {
	case "data/students.json":
		return path, filepath.Join(h.admin.cfg.DataDir, "students.json"), nil
	case "data/contests.json":
		return path, filepath.Join(h.admin.cfg.DataDir, "contests.json"), nil
	case adminIntakePath:
		return path, filepath.Join(h.admin.cfg.DataDir, "student_intake.json"), nil
	case adminIntakeStagingPath:
		return path, filepath.Join(h.admin.cfg.DataDir, "student_intake_admin.json"), nil
	}

	const groupPrefix = "data/groups/"
	if !strings.HasPrefix(path, groupPrefix) {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}

	tail := strings.TrimPrefix(path, groupPrefix)
	parts := strings.Split(tail, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}

	slug := strings.TrimSpace(parts[0])
	fileName := strings.TrimSpace(parts[1])
	if !domain.IsValidSlug(slug) {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}
	if fileName != "group.json" && fileName != "contests.json" {
		return "", "", fmt.Errorf("path %q is not allowed", path)
	}

	normalized := filepath.ToSlash(filepath.Join("data", "groups", slug, fileName))
	absolute := filepath.Join(h.admin.cfg.DataDir, "groups", slug, fileName)
	return normalized, absolute, nil
}

func decodeAdminFileRequest(r *http.Request) (adminFileRequest, error) {
	var req adminFileRequest

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return adminFileRequest{}, fmt.Errorf("invalid request body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return adminFileRequest{}, fmt.Errorf("request body must contain a single JSON object")
	}

	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		return adminFileRequest{}, fmt.Errorf("path is required")
	}

	return req, nil
}

func decodeAdminIntakeMergeRequest(r *http.Request) (adminIntakeMergeRequest, error) {
	var req adminIntakeMergeRequest

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminJSONBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return adminIntakeMergeRequest{}, fmt.Errorf("invalid request body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return adminIntakeMergeRequest{}, fmt.Errorf("request body must contain a single JSON object")
	}

	return req, nil
}

func validateJSONSyntax(body string) error {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()

	var v any
	if err := decoder.Decode(&v); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid json: trailing data after root value")
	}

	return nil
}

func newAdminResult(action string, success bool, exitCode int, started time.Time, output string, errorsList []string) AdminActionResult {
	duration := time.Since(started).Round(time.Millisecond)
	if duration < 0 {
		duration = 0
	}
	if len(errorsList) == 0 {
		errorsList = nil
	}
	return AdminActionResult{
		Action:       action,
		Success:      success,
		ExitCode:     exitCode,
		StartedAt:    started.In(moscowLocation).Format("2006-01-02 15:04:05") + " МСК",
		StartedAtISO: started.Format(time.RFC3339),
		Duration:     duration.String(),
		Output:       output,
		Errors:       errorsList,
	}
}

func commandExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func renderCommand(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteShellArg(path))
	for _, arg := range args {
		parts = append(parts, quoteShellArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteShellArg(value string) string {
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " \t\n\"'`$()[]{}|&;<>*?!") {
		return strconv.Quote(value)
	}
	return value
}
