package httpapi

import (
	"encoding/json"
	"errors"
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

func (h *Handlers) APISummary(w http.ResponseWriter, _ *http.Request) {
	summary, err := h.loader.LoadOverallStandings()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "summary not generated yet", http.StatusNotFound)
			return
		}
		h.logger.Printf("ERROR load summary: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, summary)
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
		PageTitle: "Standings: " + standings.GroupTitle,
		Standings: standings,
		Footer:    h.buildFooterInfo(),
	}
	if err := h.renderer.Render(w, http.StatusOK, "group_standings.html", page); err != nil {
		h.logger.Printf("ERROR render group standings slug=%s err=%v", slug, err)
	}
}

func (h *Handlers) IndexPage(w http.ResponseWriter, _ *http.Request) {
	summary, err := h.loader.LoadOverallStandings()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			summary = domain.GeneratedOverallStandings{}
		} else {
			h.logger.Printf("ERROR load summary for index: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	page := IndexPageData{
		PageTitle: "Olympiad Standings: Summary",
		Summary:   summary,
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
	Summary   domain.GeneratedOverallStandings
	Footer    FooterInfo
}

type GroupPageData struct {
	PageTitle string
	Standings domain.GeneratedGroupStandings
	Footer    FooterInfo
}
