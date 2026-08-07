package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	// generateInterval — ожидаемый период автогенерации (для показа «следующее
	// обновление»). 0 — расписание не задано, «следующее» не показываем.
	generateInterval time.Duration
	// dataMu сериализует read-modify-write админ-операций над data-файлами
	// (students.json, group.json, contests.json группы, grades_manual, intake
	// merge), чтобы два одновременных запроса не потеряли запись друг друга.
	dataMu sync.Mutex
	// pageCache — готовый HTML публичных страниц групп (см. page_cache.go).
	pageCache groupPageCache
	// summaryCache — готовые байты /summary-data (JSON + gzip, см. page_cache.go).
	summaryCache summaryDataCache
	// panelSecretValue — секрет подписи role-token'ов панелей групп (лениво
	// читается/создаётся в data/credentials/panel_secret.json).
	panelSecretMu    sync.Mutex
	panelSecretValue []byte
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

// directoryTokenPath — файл с секретным токеном каталога групп. Управляется из
// админки, читается на каждый запрос (смена действует сразу).
func (h *Handlers) directoryTokenPath() string {
	if h.dataDir == "" {
		return ""
	}
	return filepath.Join(h.dataDir, "credentials", "directory_credentials.json")
}

// readDirectoryToken читает текущий токен каталога (пусто — каталог выключен).
func (h *Handlers) readDirectoryToken() string {
	path := h.directoryTokenPath()
	if path == "" {
		return ""
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Token)
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
	merged := h.mergeCombinedMembers(slug, gf, visiting)
	// Контесты, скрытые в настройках объединённой группы, убираем совсем
	// (и из публичного вида, и по токену — это кураторский выбор, не заморозка).
	if len(gf.HiddenContests) > 0 {
		hidden := make(map[string]struct{}, len(gf.HiddenContests))
		for _, id := range gf.HiddenContests {
			hidden[strings.TrimSpace(id)] = struct{}{}
		}
		kept := merged.Contests[:0]
		for _, c := range merged.Contests {
			if _, drop := hidden[c.ID]; !drop {
				kept = append(kept, c)
			}
		}
		merged.Contests = kept
	}
	hideUpcomingContestTaskURLs(&merged)
	return merged, nil
}

// mergeCombinedMembers загружает таблицы групп-участниц (рекурсивно, с защитой от
// циклов) и сливает их в одну. Без фильтрации скрытых контестов — её делает
// вызывающий (для настроек нужен полный список).
func (h *Handlers) mergeCombinedMembers(slug string, gf domain.GroupFile, visiting map[string]struct{}) domain.GeneratedGroupStandings {
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
		// Подпись участников коротким названием группы-источника. Только для
		// обычных групп: вложенное объединение уже подписано своими группами.
		if mf, ok := h.readSourceGroupFile(memberSlug); ok && len(mf.MemberGroups) == 0 {
			member.GroupShortName = strings.TrimSpace(mf.ShortName)
		}
		members = append(members, member)
	}

	return domain.MergeGroupStandings(slug, title, members)
}

// applyFreezeView решает, какую версию замороженных таблиц отдавать: с верным
// group_secret_token (?token=…) — полную (просмотр жюри), иначе — публичную
// замороженную, вырезая полные варианты из ответа. Также по тому же токену
// показываются скрытые (Hidden) контесты и скрытые задачи-колонки, а без него —
// вырезаются. Возвращает true при токенном просмотре.
func (h *Handlers) applyFreezeView(standings *domain.GeneratedGroupStandings, slug string, r *http.Request) bool {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	return h.applyFreezeViewForRole(standings, slug, h.groupRole(slug, token, "").AtLeast(RoleObserver))
}

// applyFreezeViewForRole — то же по уже вычисленной роли: elevated (наблюдатель
// и выше) видит полные версии замороженных таблиц и скрытые контесты.
func (h *Handlers) applyFreezeViewForRole(standings *domain.GeneratedGroupStandings, slug string, elevated bool) bool {
	if elevated {
		standings.SwapInFullRows()
		return true
	}
	standings.StripFullRows()
	standings.StripHiddenContests()
	standings.StripHiddenTasks()
	if !h.groupShowsTaskLinks(slug) {
		standings.StripTaskLinks()
	}
	return false
}

// groupShowsTaskLinks читает флаг show_task_links из group.json (по умолчанию —
// показывать). false — на публичной странице ссылок на задачи нет.
func (h *Handlers) groupShowsTaskLinks(slug string) bool {
	gf, ok := h.readSourceGroupFile(slug)
	if !ok {
		return true
	}
	return gf.TaskLinksShown()
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

// hideUpcomingContestTaskURLs убирает ссылки и названия задач у контестов, которые
// ещё не начались (StartTime в будущем), чтобы задачи нельзя было подсмотреть до
// старта — ни на страницах, ни в JSON (API и встраиваемый в сводную страницу).
// Метки-колонки (A, B, …) остаются: виден объём контеста, но не сами задачи.
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
			contest.Tasks[j].Name = ""
		}
		for j := range contest.Subcontests {
			for k := range contest.Subcontests[j].Tasks {
				contest.Subcontests[j].Tasks[k].URL = ""
				contest.Subcontests[j].Tasks[k].NormalizedURL = ""
				contest.Subcontests[j].Tasks[k].Name = ""
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
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	role := h.groupRole(slug, token, "")
	h.renderGroupPage(w, r, slug, role, token, "")
}

// GroupPanelPage — страница группы для жюри/админа группы: вход по логину и
// паролю панели (Basic Auth), роль определяется совпавшей парой учёток. Токен
// группы подставляется сервером, чтобы ссылки внутри страницы работали.
func (h *Handlers) GroupPanelPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	role, roleToken, ok := h.authorizePanelRequest(w, r, slug)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderGroupPage(w, r, slug, role, h.groupTokenOf(slug), roleToken)
}

// renderGroupPage рендерит страницу группы под заданную роль. Публичный вид
// (RoleGuest) отдаётся из кэша готового HTML; вид по токену и панели — всегда
// свежий рендер.
func (h *Handlers) renderGroupPage(w http.ResponseWriter, r *http.Request, slug string, role GroupRole, token, roleToken string) {
	// Публичный вид (без токена) отдаём из кэша готового HTML, если входы не
	// менялись; серверное время в HTML подставляется актуальное.
	cacheVersion := ""
	if role == RoleGuest {
		if v, ok := h.groupPageVersion(slug); ok {
			cacheVersion = v
			if html, hit := h.cachedGroupPage(slug, v); hit {
				writeCachedHTML(w, html)
				return
			}
		}
	}

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

	elevated := role.AtLeast(RoleObserver)
	unfrozen := h.applyFreezeViewForRole(&standings, slug, elevated)
	page := GroupPageData{
		PageTitle:       standings.GroupTitle,
		Standings:       standings,
		Footer:          h.buildFooterInfo(),
		UnfrozenView:    unfrozen,
		TokenValid:      elevated,
		Role:            role.String(),
		RoleTitle:       role.Title(),
		RoleToken:       roleToken,
		InPanel:         roleToken != "",
		CanGrade:        role.AtLeast(RoleJury),
		CombinedMembers: h.combinedMemberTitles(slug),
	}
	if gf, ok := h.readSourceGroupFile(slug); ok {
		page.GroupArchived = gf.Archived()
		// Ссылка «войти в панель» на странице по токену — если панель настроена.
		page.PanelConfigured = gf.PanelConfigured()
	}
	if elevated {
		page.Token = token
		h.juryStandingsExtras(slug, &page, role)
	}

	if cacheVersion != "" {
		// Рендер в буфер: кладём в кэш и отдаём с актуальным server-now.
		rec := &bufferingResponseWriter{header: make(http.Header)}
		if err := h.renderer.Render(rec, http.StatusOK, "group_standings.html", page); err != nil {
			h.logger.Printf("ERROR render group standings slug=%s err=%v", slug, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		h.storeGroupPage(slug, cacheVersion, nextTimeBoundary(&standings, time.Now()), rec.buf.Bytes())
		writeCachedHTML(w, rec.buf.Bytes())
		return
	}

	if err := h.renderer.Render(w, http.StatusOK, "group_standings.html", page); err != nil {
		h.logger.Printf("ERROR render group standings slug=%s err=%v", slug, err)
	}
}

// bufferingResponseWriter копит ответ в память (для кэша готового HTML).
type bufferingResponseWriter struct {
	header http.Header
	buf    bytes.Buffer
	status int
}

func (b *bufferingResponseWriter) Header() http.Header         { return b.header }
func (b *bufferingResponseWriter) WriteHeader(code int)        { b.status = code }
func (b *bufferingResponseWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }

// GroupContestFragment отдаёт HTML-блок одной таблицы контеста для ленивой
// подгрузки на странице группы. Только публичный вид: ленивые заглушки
// рендерятся лишь без токена (жюри получает полную страницу сразу).
func (h *Handlers) GroupContestFragment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	contestID := strings.TrimSpace(r.URL.Query().Get("id"))
	if contestID == "" {
		http.NotFound(w, r)
		return
	}
	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidGroupSlug) || errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.logger.Printf("ERROR load contest fragment slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.applyFreezeView(&standings, slug, r)

	for i := range standings.Contests {
		if standings.Contests[i].ID != contestID {
			continue
		}
		archived := false
		if gf, ok := h.readSourceGroupFile(slug); ok {
			archived = gf.Archived()
		}
		data := map[string]any{
			"Contest":       standings.Contests[i],
			"TokenValid":    false,
			"JuryCanManage": false,
			"JuryKonduits":  map[string]bool(nil),
			"GroupSlug":     standings.GroupSlug,
			"Token":         "",
			"GroupArchived": archived,
		}
		if err := h.renderer.RenderFragment(w, http.StatusOK, "group_standings.html", "contestBlockBody", data); err != nil {
			h.logger.Printf("ERROR render contest fragment slug=%s id=%s err=%v", slug, contestID, err)
		}
		return
	}
	http.NotFound(w, r)
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

// GroupParticipantsPage — статистика участников группы по токену (наблюдатель).
func (h *Handlers) GroupParticipantsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if !domain.IsValidSlug(slug) || !h.groupTokenValid(slug, token) {
		http.NotFound(w, r)
		return
	}
	h.renderParticipantsPage(w, r, slug, token, false)
}

// GroupPanelParticipantsPage — те же участники, но из панели: ссылки на профили
// ведут в панель (там доступна разметка флагов).
func (h *Handlers) GroupPanelParticipantsPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	if _, _, ok := h.authorizePanelRequest(w, r, slug); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderParticipantsPage(w, r, slug, h.groupTokenOf(slug), true)
}

func (h *Handlers) renderParticipantsPage(w http.ResponseWriter, r *http.Request, slug, token string, panelView bool) {
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
	reviews := h.loadFlagReviewIndex()
	rows := make([]ParticipantRow, 0, len(studentIDs))
	for _, id := range studentIDs {
		row := ParticipantRow{StudentID: id, PublicName: id}
		if profile, err := h.loader.LoadStudentProfile(id); err == nil {
			row.HasProfile = true
			if profile.PublicName != "" {
				row.PublicName = profile.PublicName
			}
			row.Stats = profile.Stats
			row.Accounts = profile.Accounts
			// Темп именно этого курса (группы) — основной контент страницы.
			applyFlagReviews(reviews, id, profile.CourseStats)
			for i := range profile.CourseStats {
				if profile.CourseStats[i].GroupSlug == slug {
					cs := profile.CourseStats[i]
					row.Course = &cs
					break
				}
			}
		}
		rows = append(rows, row)
	}

	// Сортировка «по уму»: сначала ученики с посчитанной скоростью (по убыванию),
	// затем «мало данных» (по прогрессу), затем без профиля — по имени.
	sort.SliceStable(rows, func(a, b int) bool {
		ra, rb := rows[a], rows[b]
		ka, kb := participantSortKey(ra), participantSortKey(rb)
		if ka != kb {
			return ka < kb
		}
		switch ka {
		case 0:
			if ra.Course.Speed != rb.Course.Speed {
				return ra.Course.Speed > rb.Course.Speed
			}
		case 1:
			if ra.Course.Progress != rb.Course.Progress {
				return ra.Course.Progress > rb.Course.Progress
			}
		}
		return strings.ToLower(ra.PublicName) < strings.ToLower(rb.PublicName)
	})

	page := GroupParticipantsPageData{
		PageTitle:  title + " — участники",
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		Token:      token,
		PanelView:  panelView,
		Rows:       rows,
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_participants.html", page); err != nil {
		h.logger.Printf("ERROR render group participants slug=%s err=%v", slug, err)
	}
}

// participantSortKey: 0 — есть скорость, 1 — есть курс-статы без скорости,
// 2 — нет данных по курсу.
func participantSortKey(r ParticipantRow) int {
	switch {
	case r.Course != nil && !r.Course.LowData && r.Course.Speed > 0:
		return 0
	case r.Course != nil:
		return 1
	default:
		return 2
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
		Token:     token,
	}
	h.fillStudentProfilePage(&page, id, &slug)
	if err := h.renderer.Render(w, http.StatusOK, "admin_student.html", page); err != nil {
		h.logger.Printf("ERROR render group student profile slug=%s id=%s err=%v", slug, id, err)
	}
}

// GroupPanelStudentPage — профиль участника из панели: то же, что по токену, но
// с разметкой флагов нечестности (роль «жюри» и выше).
func (h *Handlers) GroupPanelStudentPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	role, roleToken, ok := h.authorizePanelRequest(w, r, slug)
	if !ok {
		return
	}
	if !role.AtLeast(RoleJury) || !domain.IsValidSlug(id) || !h.groupContainsStudent(slug, id) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	page := AdminStudentProfilePageData{
		Footer:         h.buildFooterInfo(),
		StudentID:      id,
		BackURL:        "/standings/" + slug + "/panel/participants",
		BackLabel:      "← Участники",
		TokenView:      true,
		Token:          h.groupTokenOf(slug),
		RoleToken:      roleToken,
		CanReviewFlags: true,
	}
	h.fillStudentProfilePage(&page, id, &slug)
	if err := h.renderer.Render(w, http.StatusOK, "admin_student.html", page); err != nil {
		h.logger.Printf("ERROR render panel student slug=%s id=%s err=%v", slug, id, err)
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

	modeTitle := "summary-edu"
	if mode == "olymp" {
		modeTitle = "summary-olymp"
	} else if mode == "all" {
		modeTitle = "summary"
	}

	// Данные сводной страница загружает отдельным запросом (/summary-data):
	// HTML остаётся лёгким, а JSON жмётся и грузится параллельно.
	page := GroupSummaryPageData{
		PageTitle:    standings.GroupTitle + " — " + modeTitle,
		GroupTitle:   standings.GroupTitle,
		GroupSlug:    standings.GroupSlug,
		Mode:         mode,
		Footer:       h.buildFooterInfo(),
		UnfrozenView: unfrozen,
	}
	if unfrozen {
		page.Token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_summary.html", page); err != nil {
		h.logger.Printf("ERROR render group summary slug=%s mode=%s err=%v", slug, mode, err)
	}
}

// GroupSummaryData отдаёт JSON standings для сводной — ровно те же данные, что
// раньше встраивались в HTML сводной (с теми же правилами видимости по токену).
func (h *Handlers) GroupSummaryData(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")

	// Ответ большой (мегабайты у больших групп) и меняется только с генерацией:
	// кэшируем готовые байты (компактный JSON + gzip) по отпечатку файлов —
	// как HTML-кэш страницы группы. Вид зависит от токена (разморозка/скрытое),
	// поэтому ключей два на группу.
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	unfrozen := token != "" && h.groupTokenValid(slug, token)
	version, cacheable := h.groupPageVersion(slug)
	cacheKey := slug + "|" + strconv.FormatBool(unfrozen)
	if cacheable {
		if e, ok := h.cachedSummaryData(cacheKey, version); ok {
			writeSummaryData(w, r, e)
			return
		}
	}

	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidGroupSlug) || errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.logger.Printf("ERROR load summary data slug=%s err=%v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.applyFreezeView(&standings, slug, r)

	plain, err := json.Marshal(standings)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	gzipped, err := gzipBytes(plain)
	if err != nil {
		h.logger.Printf("ERROR gzip summary data slug=%s err=%v", slug, err)
		gzipped = nil
	}
	e := summaryDataEntry{plain: plain, gzipped: gzipped}
	if cacheable {
		h.storeSummaryData(cacheKey, version, plain, gzipped)
	}
	writeSummaryData(w, r, e)
}

func (h *Handlers) IndexPage(w http.ResponseWriter, r *http.Request) {
	page := IndexPageData{
		PageTitle: "Standings",
		Footer:    h.buildFooterInfo(),
	}
	// Каталог групп — по директорному токену (?token=…). Неверный/пустой токен —
	// обычный экран, факт существования каталога не раскрываем.
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if dirToken := h.readDirectoryToken(); dirToken != "" && token != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(dirToken)) == 1 {
		// Активные и архивные группы — в каталоге разными списками (архив свёрнут).
		for _, g := range h.buildDirectory() {
			if g.Archived {
				page.ArchivedGroups = append(page.ArchivedGroups, g)
			} else {
				page.Directory = append(page.Directory, g)
			}
		}
	}
	if err := h.renderer.Render(w, http.StatusOK, "index.html", page); err != nil {
		h.logger.Printf("ERROR render index: %v", err)
	}
}

// buildDirectory собирает список всех настроенных групп (data/groups/*) со
// ссылками для ученика и преподавателя (с токеном группы, если он задан).
func (h *Handlers) buildDirectory() []DirectoryGroup {
	if h.dataDir == "" {
		return []DirectoryGroup{}
	}
	entries, err := os.ReadDir(filepath.Join(h.dataDir, "groups"))
	if err != nil {
		return []DirectoryGroup{}
	}
	out := make([]DirectoryGroup, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !domain.IsValidSlug(e.Name()) {
			continue
		}
		slug := e.Name()
		gf, ok := h.readSourceGroupFile(slug)
		if !ok {
			continue
		}
		title := strings.TrimSpace(gf.Title)
		if title == "" {
			title = slug
		}
		item := DirectoryGroup{
			Slug:       slug,
			Title:      title,
			StudentURL: "/standings/" + slug,
			Combined:   len(gf.MemberGroups) > 0,
			Archived:   gf.Archived(),
		}
		if tok := strings.TrimSpace(gf.GroupSecretToken); tok != "" {
			item.TeacherURL = "/standings/" + slug + "?token=" + url.QueryEscape(tok)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

func (h *Handlers) buildFooterInfo() FooterInfo {
	now := time.Now()
	// Всё показываемое время — в поясе браузера (JS перерисовывает по ISO).
	// ServerTime/LastUpdatedMSK — лишь фолбэк-текст на случай без JS (в МСК).
	footer := FooterInfo{
		ServerTime:      now.In(moscowLocation).Format("15:04:05"),
		ServerTimeISO:   now.Format(time.RFC3339),
		LastUpdatedMSK:  "—",
		IntervalSeconds: int(h.generateInterval / time.Second),
	}

	updatedAt, err := h.loader.LoadLastUpdatedAt()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			h.logger.Printf("WARN load last updated time: %v", err)
		}
		return footer
	}

	footer.LastUpdatedMSK = updatedAt.In(moscowLocation).Format("02.01.2006 15:04:05") + " МСК"
	footer.LastUpdatedISO = updatedAt.Format(time.RFC3339)
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

// ConfigureGenerateInterval задаёт ожидаемый период автогенерации.
func (h *Handlers) ConfigureGenerateInterval(d time.Duration) {
	if d > 0 {
		h.generateInterval = d
	}
}

type FooterInfo struct {
	LastUpdatedMSK string
	ServerTime     string
	// Машиночитаемые значения для «живого» футера (JS): ISO-время последней
	// генерации и серверное «сейчас» (для синхронизации часов клиента), и период
	// автогенерации в секундах (0 — расписание не задано).
	LastUpdatedISO  string
	ServerTimeISO   string
	IntervalSeconds int
}

type IndexPageData struct {
	PageTitle string
	Footer    FooterInfo
	// Directory — активные группы каталога (по директорному токену). nil — обычный
	// экран. ArchivedGroups — группы в архиве (показываются свёрнутым блоком).
	Directory      []DirectoryGroup
	ArchivedGroups []DirectoryGroup
}

// DirectoryGroup — одна строка каталога групп: ссылка для ученика и (если есть
// токен группы) ссылка для преподавателя с токеном.
type DirectoryGroup struct {
	Slug       string
	Title      string
	StudentURL string
	TeacherURL string // пусто — у группы нет токена жюри
	Combined   bool
	Archived   bool // группа в архиве (update=false): в каталоге — в свёрнутом блоке
}

type GroupPageData struct {
	PageTitle string
	Standings domain.GeneratedGroupStandings
	Footer    FooterInfo
	// UnfrozenView — просмотр по токену группы: показана полная версия
	// замороженных таблиц. Token протаскивается в ссылки страницы.
	UnfrozenView bool
	Token        string
	// TokenValid — доступ не ниже наблюдателя: полные (размороженные) таблицы,
	// скрытые контесты, статистика участников, экспорт.
	TokenValid bool
	// Role/RoleTitle — роль запроса («observer»/«jury»/«admin») и её название
	// для шапки страницы. RoleToken — подпись роли для API панели (пусто вне
	// панели). InPanel — страница открыта из панели (по логину и паролю).
	Role      string
	RoleTitle string
	RoleToken string
	InPanel   bool
	// CanGrade — роль не ниже жюри: ручные оценки, кондуиты, настройка таблицы
	// оценок, разметка флагов.
	CanGrade bool
	// PanelConfigured — у группы заданы учётки панели: наблюдателю показываем
	// ссылку «войти как жюри/админ».
	PanelConfigured bool
	// CombinedMembers — названия групп-участниц, если это объединённая группа
	// (для подписи на странице). Пусто — обычная группа.
	CombinedMembers []string
	// GroupArchived — группа в архиве (update=false): под всеми её таблицами
	// показываем «последнее обновление» (таблицы не пересобираются).
	GroupArchived bool
	// Жюри-панель (заполняется только при валидном токене):
	// JuryCanManage — можно добавлять/двигать контесты (обычная группа);
	// JuryAddable — глобальные контесты, которых ещё нет в группе;
	// JuryKonduits — id inline-кондуитов (жюри может заполнять оценки);
	// JuryHasGrades — у группы есть ручные столбцы оценок (ссылка на редактор).
	JuryCanManage bool
	JuryAddable   []AdminGroupContestOption
	JuryKonduits  map[string]bool
	JuryHasGrades bool
	// JuryNewKonduits — кондуиты группы, которых ещё нет в сгенерированных
	// таблицах (только что созданы): ссылки на редактор показываются в панели.
	JuryNewKonduits []AdminGroupContestOption
}

type ParticipantRow struct {
	StudentID  string
	PublicName string
	HasProfile bool
	Stats      domain.StudentActivityStats
	// Accounts — аккаунты ученика: для ссылок «посылки ученика по задаче» у
	// сигналов застревания/пропуска.
	Accounts []domain.Account
	// Course — темп именно этого курса (группы) из профиля ученика; nil — не
	// посчитан (нет посылок или профиль ещё не сгенерирован).
	Course *domain.StudentCourseStats
}

type GroupParticipantsPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	Token      string
	// PanelView — открыто из панели: ссылки на профили ведут в панель, где
	// доступна разметка флагов.
	PanelView bool
	Rows      []ParticipantRow
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
	PageTitle    string
	GroupTitle   string
	GroupSlug    string
	Mode         string
	Footer       FooterInfo
	UnfrozenView bool
	Token        string
}
