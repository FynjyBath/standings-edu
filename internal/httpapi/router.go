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
	mux.HandleFunc("POST /api/rpc", handlers.StandingsRPC)
	mux.HandleFunc("GET /standings/admin", handlers.AdminAuth(handlers.AdminPage))
	mux.HandleFunc("POST /api/admin/actions/update", handlers.AdminAuth(handlers.AdminActionUpdate))
	mux.HandleFunc("POST /api/admin/actions/generate", handlers.AdminAuth(handlers.AdminActionGenerate))
	mux.HandleFunc("POST /api/admin/actions/clear-cache", handlers.AdminAuth(handlers.AdminActionClearCache))
	mux.HandleFunc("POST /api/admin/actions/intake/prepare", handlers.AdminAuth(handlers.AdminIntakeStagingPrepare))
	mux.HandleFunc("POST /api/admin/actions/intake/merge", handlers.AdminAuth(handlers.AdminIntakeStagingMerge))
	mux.HandleFunc("POST /api/admin/groups/create", handlers.AdminAuth(handlers.AdminGroupCreate))
	mux.HandleFunc("GET /api/admin/files", handlers.AdminAuth(handlers.AdminFiles))
	mux.HandleFunc("GET /api/admin/file", handlers.AdminAuth(handlers.AdminFile))
	mux.HandleFunc("POST /api/admin/file/validate", handlers.AdminAuth(handlers.AdminFileValidate))
	mux.HandleFunc("POST /api/admin/file/save", handlers.AdminAuth(handlers.AdminFileSave))
	mux.HandleFunc("GET /standings", handlers.IndexPage)
	mux.HandleFunc("GET /standings/{group_name}", handlers.GroupStandingsPage)
	mux.HandleFunc("GET /standings/{group_name}/summary", handlers.GroupSummaryAllPage)
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
