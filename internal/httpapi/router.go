package httpapi

import "net/http"

func NewRouter(handlers *Handlers, staticDir string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handlers.Healthz)
	mux.HandleFunc("GET /api/summary", handlers.APISummary)
	mux.HandleFunc("GET /api/groups", handlers.APIGroups)
	mux.HandleFunc("GET /api/groups/{group_name}/standings", handlers.APIGroupStandings)
	mux.HandleFunc("GET /standings", handlers.IndexPage)
	mux.HandleFunc("GET /standings/{group_name}", handlers.GroupStandingsPage)

	staticFS := http.FileServer(http.Dir(staticDir))
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticFS))

	return mux
}
