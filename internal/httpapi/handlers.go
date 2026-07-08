package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
	"standings-edu/internal/studentintake"
	"standings-edu/internal/web"
)

var moscowLocation = loadMoscowLocation()

func loadMoscowLocation() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*60*60)
	}
	return loc
}

type Handlers struct {
	loader      *storage.GeneratedLoader
	intake      *studentintake.Store
	renderer    *web.TemplateRenderer
	logger      *log.Logger
	admin       *adminState
	intakeToken string
	dataDir     string
	// dataMu сериализует read-modify-write админ-операций над data-файлами
	// (students.json, group.json, contests.json группы, grades_manual, intake
	// merge), чтобы два одновременных запроса не потеряли запись друг друга.
	dataMu sync.Mutex
}

// SerializeDataWrite оборачивает мутирующий data-файлы админ-хендлер общим
// мьютексом. Все такие операции выполняются строго по одной.
func (h *Handlers) SerializeDataWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.dataMu.Lock()
		defer h.dataMu.Unlock()
		next(w, r)
	}
}

// ConfigureIntakeToken задаёт секретный токен для POST /api/rpc. Пустой токен —
// защита выключена (эндпоинт открыт).
func (h *Handlers) ConfigureIntakeToken(token string) {
	h.intakeToken = token
}

// ConfigureSourceDir задаёт каталог исходных данных (data/). Нужен, чтобы
// показывать пустую страницу только что созданной группы (со ссылкой на форму)
// ещё до первой генерации.
func (h *Handlers) ConfigureSourceDir(dataDir string) {
	h.dataDir = strings.TrimSpace(dataDir)
}

// loadGroupStandings возвращает сгенерированные standings группы, а если файла
// ещё нет — пустую таблицу, собранную из data/groups/<slug>/group.json (чтобы
// страница новой группы открывалась со ссылкой на форму). Если группы нет нигде —
// возвращает os.ErrNotExist.
func (h *Handlers) loadGroupStandings(slug string) (domain.GeneratedGroupStandings, error) {
	return h.loadGroupStandingsGuarded(slug, nil)
}

func (h *Handlers) loadGroupStandingsGuarded(slug string, visiting map[string]struct{}) (domain.GeneratedGroupStandings, error) {
	// Объединённая группа (member_groups в group.json) собирается на лету из
	// таблиц групп-участниц.
	if gf, ok := h.readSourceGroupFile(slug); ok && len(gf.MemberGroups) > 0 {
		if _, cycle := visiting[slug]; cycle {
			return domain.GeneratedGroupStandings{}, storage.ErrInvalidGroupSlug
		}
		return h.loadCombinedGroupStandings(slug, gf, visiting)
	}

	standings, err := h.loader.LoadGroupStandings(slug)
	if err == nil {
		hideUpcomingContestTaskURLs(&standings)
		return standings, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return domain.GeneratedGroupStandings{}, err
	}

	empty, ok, srcErr := h.loadEmptyGroupFromSource(slug)
	if srcErr != nil {
		h.logger.Printf("WARN load source group slug=%s err=%v", slug, srcErr)
		return domain.GeneratedGroupStandings{}, err
	}
	if !ok {
		return domain.GeneratedGroupStandings{}, err
	}
	return empty, nil
}

// loadCombinedGroupStandings собирает таблицы объединённой группы: загружает
// сгенерированные таблицы каждой группы-участницы и сливает их (общие по ссылке
// контесты — в одну таблицу). Недоступные участницы (ещё не сгенерированы)
// пропускаются. Если ни одной таблицы нет — отдаём пустую страницу.
func (h *Handlers) loadCombinedGroupStandings(slug string, gf domain.GroupFile, visiting map[string]struct{}) (domain.GeneratedGroupStandings, error) {
	title := strings.TrimSpace(gf.Title)
	if title == "" {
		title = slug
	}

	nextVisiting := make(map[string]struct{}, len(visiting)+1)
	for k := range visiting {
		nextVisiting[k] = struct{}{}
	}
	nextVisiting[slug] = struct{}{}

	members := make([]domain.GeneratedGroupStandings, 0, len(gf.MemberGroups))
	for _, memberSlug := range gf.MemberGroups {
		memberSlug = strings.TrimSpace(memberSlug)
		if memberSlug == "" || memberSlug == slug {
			continue
		}
		member, err := h.loadGroupStandingsGuarded(memberSlug, nextVisiting)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, storage.ErrInvalidGroupSlug) {
				h.logger.Printf("WARN combined group=%s member=%s load failed: %v", slug, memberSlug, err)
			}
			continue
		}
		members = append(members, member)
	}

	merged := domain.MergeGroupStandings(slug, title, members)
	hideUpcomingContestTaskURLs(&merged)
	return merged, nil
}

// applyFreezeView решает, какую версию замороженных таблиц отдавать: с верным
// group_secret_token (?token=…) — полную (просмотр жюри), иначе — публичную
// замороженную, вырезая полные варианты из ответа. Также по тому же токену
// показываются скрытые (Hidden) контесты, а без него — вырезаются. Возвращает
// true при токенном просмотре.
func (h *Handlers) applyFreezeView(standings *domain.GeneratedGroupStandings, slug string, r *http.Request) bool {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token != "" && h.groupTokenValid(slug, token) {
		standings.SwapInFullRows()
		return true
	}
	standings.StripFullRows()
	standings.StripHiddenContests()
	return false
}

// groupTokenValid сверяет токен с group_secret_token из groups/<slug>/group.json.
func (h *Handlers) groupTokenValid(slug, token string) bool {
	if h.dataDir == "" || !domain.IsValidSlug(slug) {
		return false
	}
	var groupFile domain.GroupFile
	path := filepath.Join(h.dataDir, "groups", slug, "group.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(body, &groupFile); err != nil {
		return false
	}
	expected := strings.TrimSpace(groupFile.GroupSecretToken)
	if expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

// hideUpcomingContestTaskURLs убирает ссылки на задачи у контестов, которые ещё
// не начались (StartTime в будущем), чтобы задачи нельзя было подсмотреть до
// старта — ни на страницах, ни в JSON (API и встраиваемый в сводную страницу).
// Названия задач остаются: виден объём контеста, но не условия.
func hideUpcomingContestTaskURLs(standings *domain.GeneratedGroupStandings) {
	now := time.Now()
	for i := range standings.Contests {
		contest := &standings.Contests[i]
		if contest.StartTime == nil || !now.Before(*contest.StartTime) {
			continue
		}
		for j := range contest.Tasks {
			contest.Tasks[j].URL = ""
			contest.Tasks[j].NormalizedURL = ""
		}
		for j := range contest.Subcontests {
			for k := range contest.Subcontests[j].Tasks {
				contest.Subcontests[j].Tasks[k].URL = ""
				contest.Subcontests[j].Tasks[k].NormalizedURL = ""
			}
		}
	}
}

func (h *Handlers) loadEmptyGroupFromSource(slug string) (domain.GeneratedGroupStandings, bool, error) {
	if h.dataDir == "" || !domain.IsValidSlug(slug) {
		return domain.GeneratedGroupStandings{}, false, nil
	}

	path := filepath.Join(h.dataDir, "groups", slug, "group.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.GeneratedGroupStandings{}, false, nil
		}
		return domain.GeneratedGroupStandings{}, false, err
	}

	var gf domain.GroupFile
	if err := json.Unmarshal(body, &gf); err != nil {
		return domain.GeneratedGroupStandings{}, false, err
	}

	title := strings.TrimSpace(gf.Title)
	if title == "" {
		title = slug
	}
	return domain.GeneratedGroupStandings{
		GroupSlug:  slug,
		GroupTitle: title,
		FormLink:   strings.TrimSpace(gf.FormLink),
		Contests:   nil,
	}, true, nil
}

func NewHandlers(loader *storage.GeneratedLoader, intake *studentintake.Store, renderer *web.TemplateRenderer, logger *log.Logger) *Handlers {
	if logger == nil {
		logger = log.Default()
	}
	return &Handlers{
		loader:   loader,
		intake:   intake,
		renderer: renderer,
		logger:   logger,
	}
}

func (h *Handlers) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handlers) APIGroups(w http.ResponseWriter, _ *http.Request) {
	groups, err := h.loader.LoadGroups()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "groups not generated yet", http.StatusNotFound)
			return
		}
		h.logger.Printf("ERROR load groups: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (h *Handlers) APIGroupStandings(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidGroupSlug) {
			http.Error(w, "group standings not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "group standings not found", http.StatusNotFound)
			return
		}
		h.logger.Printf("ERROR load standings slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.applyFreezeView(&standings, slug, r)
	writeJSON(w, http.StatusOK, standings)
}

func (h *Handlers) GroupStandingsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidGroupSlug) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.logger.Printf("ERROR load standings page slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	unfrozen := h.applyFreezeView(&standings, slug, r)
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	tokenValid := token != "" && h.groupTokenValid(slug, token)
	page := GroupPageData{
		PageTitle:       standings.GroupTitle,
		Standings:       standings,
		Footer:          h.buildFooterInfo(),
		UnfrozenView:    unfrozen,
		TokenValid:      tokenValid,
		CombinedMembers: h.combinedMemberTitles(slug),
	}
	if tokenValid {
		page.Token = token
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_standings.html", page); err != nil {
		h.logger.Printf("ERROR render group standings slug=%s err=%v", slug, err)
	}
}

// combinedMemberTitles возвращает названия групп-участниц объединённой группы
// (для подписи на странице). Для обычной группы — nil.
func (h *Handlers) combinedMemberTitles(slug string) []string {
	gf, ok := h.readSourceGroupFile(slug)
	if !ok || len(gf.MemberGroups) == 0 {
		return nil
	}
	titles := make([]string, 0, len(gf.MemberGroups))
	for _, member := range gf.MemberGroups {
		title := strings.TrimSpace(member)
		if mf, ok := h.readSourceGroupFile(member); ok {
			if t := strings.TrimSpace(mf.Title); t != "" {
				title = t
			}
		}
		titles = append(titles, title)
	}
	return titles
}

// GroupParticipantsPage — статистика участников группы по токену жюри.
func (h *Handlers) GroupParticipantsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if !domain.IsValidSlug(slug) || !h.groupTokenValid(slug, token) {
		http.NotFound(w, r)
		return
	}
	gf, ok := h.readSourceGroupFile(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	title := strings.TrimSpace(gf.Title)
	if title == "" {
		title = slug
	}

	studentIDs := h.resolveGroupStudentIDs(slug)
	rows := make([]ParticipantRow, 0, len(studentIDs))
	for _, id := range studentIDs {
		row := ParticipantRow{StudentID: id, PublicName: id}
		if profile, err := h.loader.LoadStudentProfile(id); err == nil {
			row.HasProfile = true
			if profile.PublicName != "" {
				row.PublicName = profile.PublicName
			}
			row.Stats = profile.Stats
		}
		rows = append(rows, row)
	}

	page := GroupParticipantsPageData{
		PageTitle:  title + " — участники",
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		Token:      token,
		Rows:       rows,
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_participants.html", page); err != nil {
		h.logger.Printf("ERROR render group participants slug=%s err=%v", slug, err)
	}
}

// GroupStudentProfilePage — профиль участника по токену группы (только член группы,
// в позициях показывается только эта группа).
func (h *Handlers) GroupStudentProfilePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !domain.IsValidSlug(slug) || !h.groupTokenValid(slug, token) || !domain.IsValidSlug(id) {
		http.NotFound(w, r)
		return
	}
	if !h.groupContainsStudent(slug, id) {
		http.NotFound(w, r)
		return
	}

	page := AdminStudentProfilePageData{
		Footer:    h.buildFooterInfo(),
		StudentID: id,
		BackURL:   "/standings/" + slug + "/participants?token=" + url.QueryEscape(token),
		BackLabel: "← Участники",
		TokenView: true,
	}
	h.fillStudentProfilePage(&page, id, &slug)
	if err := h.renderer.Render(w, http.StatusOK, "admin_student.html", page); err != nil {
		h.logger.Printf("ERROR render group student profile slug=%s id=%s err=%v", slug, id, err)
	}
}

// readSourceGroupFile читает data/groups/<slug>/group.json (исходник, для состава).
func (h *Handlers) readSourceGroupFile(slug string) (domain.GroupFile, bool) {
	if h.dataDir == "" || !domain.IsValidSlug(slug) {
		return domain.GroupFile{}, false
	}
	body, err := os.ReadFile(filepath.Join(h.dataDir, "groups", slug, "group.json"))
	if err != nil {
		return domain.GroupFile{}, false
	}
	var gf domain.GroupFile
	if err := json.Unmarshal(body, &gf); err != nil {
		return domain.GroupFile{}, false
	}
	return gf, true
}

// resolveGroupStudentIDs возвращает состав группы. Для объединённой группы —
// объединение составов групп-участниц (рекурсивно, с защитой от циклов), для
// обычной — её student_ids. Порядок сохраняется, дубликаты убираются.
func (h *Handlers) resolveGroupStudentIDs(slug string) []string {
	return h.resolveGroupStudentIDsGuarded(slug, make(map[string]struct{}), make(map[string]struct{}))
}

func (h *Handlers) resolveGroupStudentIDsGuarded(slug string, seen, visiting map[string]struct{}) []string {
	gf, ok := h.readSourceGroupFile(slug)
	if !ok {
		return nil
	}
	if len(gf.MemberGroups) == 0 {
		out := make([]string, 0, len(gf.StudentIDs))
		for _, id := range domain.NormalizeGroups(gf.StudentIDs) {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		return out
	}
	if _, cycle := visiting[slug]; cycle {
		return nil
	}
	visiting[slug] = struct{}{}
	out := make([]string, 0)
	for _, member := range gf.MemberGroups {
		out = append(out, h.resolveGroupStudentIDsGuarded(member, seen, visiting)...)
	}
	delete(visiting, slug)
	return out
}

// groupContainsStudent проверяет членство ученика в группе с учётом объединённых
// групп (по резолвнутому составу).
func (h *Handlers) groupContainsStudent(slug, id string) bool {
	for _, sid := range h.resolveGroupStudentIDs(slug) {
		if sid == id {
			return true
		}
	}
	return false
}

func (h *Handlers) GroupGradesPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidGroupSlug) || errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.logger.Printf("ERROR load grades page slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	unfrozen := h.applyFreezeView(&standings, slug, r)
	if standings.Grades == nil {
		http.NotFound(w, r)
		return
	}

	title := standings.GroupTitle
	if standings.Grades.Title != "" {
		title = title + " — " + standings.Grades.Title
	}
	page := GroupGradesPageData{
		PageTitle:    title,
		Footer:       h.buildFooterInfo(),
		GroupSlug:    standings.GroupSlug,
		GroupTitle:   standings.GroupTitle,
		Grades:       *standings.Grades,
		UnfrozenView: unfrozen,
	}
	if unfrozen {
		page.Token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_grades.html", page); err != nil {
		h.logger.Printf("ERROR render grades slug=%s err=%v", slug, err)
	}
}

func (h *Handlers) GroupSummaryEduPage(w http.ResponseWriter, r *http.Request) {
	h.renderGroupSummaryPage(w, r, "edu")
}

func (h *Handlers) GroupSummaryAllPage(w http.ResponseWriter, r *http.Request) {
	h.renderGroupSummaryPage(w, r, "all")
}

func (h *Handlers) GroupSummaryOlympPage(w http.ResponseWriter, r *http.Request) {
	h.renderGroupSummaryPage(w, r, "olymp")
}

func (h *Handlers) renderGroupSummaryPage(w http.ResponseWriter, r *http.Request, mode string) {
	slug := r.PathValue("group_name")
	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidGroupSlug) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.logger.Printf("ERROR load standings summary page slug=%s mode=%s err=%v", slug, mode, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	unfrozen := h.applyFreezeView(&standings, slug, r)
	standingsJSON, err := json.Marshal(standings)
	if err != nil {
		h.logger.Printf("ERROR marshal standings summary slug=%s mode=%s err=%v", slug, mode, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	modeTitle := "summary-edu"
	if mode == "olymp" {
		modeTitle = "summary-olymp"
	} else if mode == "all" {
		modeTitle = "summary"
	}

	page := GroupSummaryPageData{
		PageTitle:     standings.GroupTitle + " — " + modeTitle,
		GroupTitle:    standings.GroupTitle,
		GroupSlug:     standings.GroupSlug,
		Mode:          mode,
		StandingsJSON: template.JS(string(standingsJSON)),
		Footer:        h.buildFooterInfo(),
		UnfrozenView:  unfrozen,
	}
	if unfrozen {
		page.Token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_summary.html", page); err != nil {
		h.logger.Printf("ERROR render group summary slug=%s mode=%s err=%v", slug, mode, err)
	}
}

func (h *Handlers) IndexPage(w http.ResponseWriter, _ *http.Request) {
	page := IndexPageData{
		PageTitle: "Standings",
		Footer:    h.buildFooterInfo(),
	}
	if err := h.renderer.Render(w, http.StatusOK, "index.html", page); err != nil {
		h.logger.Printf("ERROR render index: %v", err)
	}
}

func (h *Handlers) buildFooterInfo() FooterInfo {
	now := time.Now()
	footer := FooterInfo{
		ServerTime:     now.Format("02.01.2006 15:04:05 MST"),
		LastUpdatedMSK: "—",
	}

	updatedAt, err := h.loader.LoadLastUpdatedAt()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			h.logger.Printf("WARN load last updated time: %v", err)
		}
		return footer
	}

	footer.LastUpdatedMSK = updatedAt.In(moscowLocation).Format("02.01.2006 15:04:05 MST")
	return footer
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write(append(b, '\n'))
}

type FooterInfo struct {
	LastUpdatedMSK string
	ServerTime     string
}

type IndexPageData struct {
	PageTitle string
	Footer    FooterInfo
}

type GroupPageData struct {
	PageTitle string
	Standings domain.GeneratedGroupStandings
	Footer    FooterInfo
	// UnfrozenView — просмотр по токену группы: показана полная версия
	// замороженных таблиц. Token протаскивается в ссылки страницы.
	UnfrozenView bool
	Token        string
	// TokenValid — токен группы верный: доступна статистика участников (даже
	// если замороженных таблиц нет и UnfrozenView=false).
	TokenValid bool
	// CombinedMembers — названия групп-участниц, если это объединённая группа
	// (для подписи на странице). Пусто — обычная группа.
	CombinedMembers []string
}

type ParticipantRow struct {
	StudentID  string
	PublicName string
	HasProfile bool
	Stats      domain.StudentActivityStats
}

type GroupParticipantsPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	Token      string
	Rows       []ParticipantRow
}

type GroupGradesPageData struct {
	PageTitle    string
	Footer       FooterInfo
	GroupSlug    string
	GroupTitle   string
	Grades       domain.GeneratedGrades
	UnfrozenView bool
	Token        string
}

type GroupSummaryPageData struct {
	PageTitle     string
	GroupTitle    string
	GroupSlug     string
	Mode          string
	StandingsJSON template.JS
	Footer        FooterInfo
	UnfrozenView  bool
	Token         string
}
