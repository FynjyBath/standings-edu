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
	mux.HandleFunc("GET /standings/admin/group-accounts", handlers.AdminAuth(handlers.AdminGroupAccounts))
	mux.HandleFunc("GET /standings/admin/group-grades", handlers.AdminAuth(handlers.AdminGroupGradesPage))
	mux.HandleFunc("POST /api/admin/group-grades/save", handlers.AdminAuth(handlers.AdminGroupGradesSave))
	mux.HandleFunc("GET /standings/admin/students", handlers.AdminAuth(handlers.AdminStudentsPage))
	mux.HandleFunc("POST /api/admin/students/save", handlers.AdminAuth(handlers.AdminStudentSave))
	mux.HandleFunc("POST /api/admin/students/delete", handlers.AdminAuth(handlers.AdminStudentDelete))
	mux.HandleFunc("GET /standings/admin/group", handlers.AdminAuth(handlers.AdminGroupManagePage))
	mux.HandleFunc("POST /api/admin/group/members/remove", handlers.AdminAuth(handlers.AdminGroupMemberRemove))
	mux.HandleFunc("POST /api/admin/group/token/set", handlers.AdminAuth(handlers.AdminGroupTokenSet))
	mux.HandleFunc("POST /api/admin/group/contests/add-ref", handlers.AdminAuth(handlers.AdminGroupContestAddRef))
	mux.HandleFunc("POST /api/admin/group/contests/remove", handlers.AdminAuth(handlers.AdminGroupContestRemove))
	mux.HandleFunc("POST /api/admin/group/contests/set-options", handlers.AdminAuth(handlers.AdminGroupContestSetOptions))
	mux.HandleFunc("POST /api/admin/group/contests/inline-save", handlers.AdminAuth(handlers.AdminGroupContestInlineSave))
	mux.HandleFunc("GET /standings/admin/contests", handlers.AdminAuth(handlers.AdminContestsPage))
	mux.HandleFunc("POST /api/admin/contests/save", handlers.AdminAuth(handlers.AdminContestSave))
	mux.HandleFunc("POST /api/admin/contests/delete", handlers.AdminAuth(handlers.AdminContestDelete))
	mux.HandleFunc("POST /api/admin/actions/generate", handlers.AdminAuth(handlers.AdminActionGenerate))
	mux.HandleFunc("POST /api/admin/actions/intake/prepare", handlers.AdminAuth(handlers.AdminIntakeStagingPrepare))
	mux.HandleFunc("POST /api/admin/actions/intake/merge", handlers.AdminAuth(handlers.AdminIntakeStagingMerge))
	mux.HandleFunc("POST /api/admin/groups/create", handlers.AdminAuth(handlers.AdminGroupCreate))
	mux.HandleFunc("GET /api/admin/files", handlers.AdminAuth(handlers.AdminFiles))
	mux.HandleFunc("GET /api/admin/file", handlers.AdminAuth(handlers.AdminFile))
	mux.HandleFunc("POST /api/admin/file/validate", handlers.AdminAuth(handlers.AdminFileValidate))
	mux.HandleFunc("POST /api/admin/file/save", handlers.AdminAuth(handlers.AdminFileSave))
	mux.HandleFunc("GET /standings", handlers.IndexPage)
	mux.HandleFunc("GET /standings/{group_name}", handlers.GroupStandingsPage)
	mux.HandleFunc("GET /standings/{group_name}/grades", handlers.GroupGradesPage)
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
