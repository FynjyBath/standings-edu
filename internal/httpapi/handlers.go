package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	page := GroupPageData{
		PageTitle: standings.GroupTitle,
		Standings: standings,
		Footer:    h.buildFooterInfo(),
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_standings.html", page); err != nil {
		h.logger.Printf("ERROR render group standings slug=%s err=%v", slug, err)
	}
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
	if standings.Grades == nil {
		http.NotFound(w, r)
		return
	}

	title := standings.GroupTitle
	if standings.Grades.Title != "" {
		title = title + " — " + standings.Grades.Title
	}
	page := GroupGradesPageData{
		PageTitle:  title,
		Footer:     h.buildFooterInfo(),
		GroupSlug:  standings.GroupSlug,
		GroupTitle: standings.GroupTitle,
		Grades:     *standings.Grades,
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
}

type GroupGradesPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	Grades     domain.GeneratedGrades
}

type GroupSummaryPageData struct {
	PageTitle     string
	GroupTitle    string
	GroupSlug     string
	Mode          string
	StandingsJSON template.JS
	Footer        FooterInfo
}
