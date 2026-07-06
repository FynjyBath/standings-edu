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
	mux.HandleFunc("POST /api/admin/group-grades/save", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupGradesSave)))
	mux.HandleFunc("GET /standings/admin/students", handlers.AdminAuth(handlers.AdminStudentsPage))
	mux.HandleFunc("POST /api/admin/students/save", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminStudentSave)))
	mux.HandleFunc("POST /api/admin/students/delete", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminStudentDelete)))
	mux.HandleFunc("GET /standings/admin/group", handlers.AdminAuth(handlers.AdminGroupManagePage))
	mux.HandleFunc("POST /api/admin/group/members/remove", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupMemberRemove)))
	mux.HandleFunc("POST /api/admin/group/token/set", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupTokenSet)))
	mux.HandleFunc("POST /api/admin/group/contests/add-ref", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupContestAddRef)))
	mux.HandleFunc("POST /api/admin/group/contests/remove", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupContestRemove)))
	mux.HandleFunc("POST /api/admin/group/contests/set-options", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupContestSetOptions)))
	mux.HandleFunc("POST /api/admin/group/contests/inline-save", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupContestInlineSave)))
	mux.HandleFunc("GET /standings/admin/contests", handlers.AdminAuth(handlers.AdminContestsPage))
	mux.HandleFunc("POST /api/admin/contests/save", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminContestSave)))
	mux.HandleFunc("POST /api/admin/contests/delete", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminContestDelete)))
	mux.HandleFunc("POST /api/admin/actions/generate", handlers.AdminAuth(handlers.AdminActionGenerate))
	mux.HandleFunc("POST /api/admin/actions/intake/prepare", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminIntakeStagingPrepare)))
	mux.HandleFunc("POST /api/admin/actions/intake/merge", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminIntakeStagingMerge)))
	mux.HandleFunc("POST /api/admin/groups/create", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminGroupCreate)))
	mux.HandleFunc("GET /api/admin/files", handlers.AdminAuth(handlers.AdminFiles))
	mux.HandleFunc("GET /api/admin/file", handlers.AdminAuth(handlers.AdminFile))
	mux.HandleFunc("POST /api/admin/file/validate", handlers.AdminAuth(handlers.AdminFileValidate))
	mux.HandleFunc("POST /api/admin/file/save", handlers.AdminAuth(handlers.SerializeDataWrite(handlers.AdminFileSave)))
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
