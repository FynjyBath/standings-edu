package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
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
	loader   *storage.GeneratedLoader
	renderer *web.TemplateRenderer
	logger   *log.Logger
}

func NewHandlers(loader *storage.GeneratedLoader, renderer *web.TemplateRenderer, logger *log.Logger) *Handlers {
	if logger == nil {
		logger = log.Default()
	}
	return &Handlers{
		loader:   loader,
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
	standings, err := h.loader.LoadGroupStandings(slug)
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
	standings, err := h.loader.LoadGroupStandings(slug)
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

func (h *Handlers) GroupSummaryEduPage(w http.ResponseWriter, r *http.Request) {
	h.renderGroupSummaryPage(w, r, "edu")
}

func (h *Handlers) GroupSummaryOlympPage(w http.ResponseWriter, r *http.Request) {
	h.renderGroupSummaryPage(w, r, "olymp")
}

func (h *Handlers) renderGroupSummaryPage(w http.ResponseWriter, r *http.Request, mode string) {
	slug := r.PathValue("group_name")
	standings, err := h.loader.LoadGroupStandings(slug)
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
		PageTitle: "Доска почёта",
		Footer:    h.buildFooterInfo(),
	}
	if err := h.renderer.Render(w, http.StatusOK, "index.html", page); err != nil {
		h.logger.Printf("ERROR render index: %v", err)
	}
}

func (h *Handlers) buildFooterInfo() FooterInfo {
	now := time.Now()
	footer := FooterInfo{
		Contact:        "t.me/fynjybath",
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
	Contact        string
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

type GroupSummaryPageData struct {
	PageTitle     string
	GroupTitle    string
	GroupSlug     string
	Mode          string
	StandingsJSON template.JS
	Footer        FooterInfo
}
