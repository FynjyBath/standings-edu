package httpapi

import (
	"net/http"
	"path/filepath"
)

func NewRouter(handlers *Handlers, staticDir string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handlers.Healthz)
	mux.HandleFunc("GET /api/groups", handlers.APIGroups)
	mux.HandleFunc("GET /api/groups/{group_name}/standings", handlers.APIGroupStandings)
	mux.HandleFunc("GET /standings", handlers.IndexPage)
	mux.HandleFunc("GET /standings/{group_name}", handlers.GroupStandingsPage)
	mux.HandleFunc("GET /standings/{group_name}/summary-edu", handlers.GroupSummaryEduPage)
	mux.HandleFunc("GET /standings/{group_name}/summary-olymp", handlers.GroupSummaryOlympPage)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(staticDir, "favicon.ico"))
	})
	mux.HandleFunc("GET /apple-touch-icon.png", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(staticDir, "apple-touch-icon.png"))
	})

	staticFS := http.FileServer(http.Dir(staticDir))
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticFS))

	return mux
}
