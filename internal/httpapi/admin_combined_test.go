package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"standings-edu/internal/domain"
)

func readGroupFileRaw(t *testing.T, dataDir, slug string) domain.GroupFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dataDir, "groups", slug, "group.json"))
	if err != nil {
		t.Fatalf("read group.json: %v", err)
	}
	var gf domain.GroupFile
	if err := json.Unmarshal(body, &gf); err != nil {
		t.Fatalf("parse group.json: %v; body=%s", err, body)
	}
	return gf
}

// Скрытие/показ контеста в объединённой группе пишется в hidden_contests и
// сохраняет member_groups; на обычной группе — отказ.
func TestAdminCombinedSetContestHidden(t *testing.T) {
	h, dataDir := newTestHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "groups", "combo", "group.json"),
		`{"title":"Объединение","member_groups":["grp_a","grp_b"]}`)

	// Скрыть c2.
	code, resp := postJSON(t, h.AdminCombinedSetContestHidden, `{"slug":"combo","contest_id":"c2","hidden":true}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("hide failed: code=%d resp=%v", code, resp)
	}
	gf := readGroupFileRaw(t, dataDir, "combo")
	if len(gf.HiddenContests) != 1 || gf.HiddenContests[0] != "c2" {
		t.Fatalf("hidden_contests wrong: %+v", gf.HiddenContests)
	}
	if len(gf.MemberGroups) != 2 {
		t.Fatalf("member_groups must be preserved: %+v", gf.MemberGroups)
	}

	// Скрыть тот же дважды — без дублей.
	postJSON(t, h.AdminCombinedSetContestHidden, `{"slug":"combo","contest_id":"c2","hidden":true}`)
	if gf := readGroupFileRaw(t, dataDir, "combo"); len(gf.HiddenContests) != 1 {
		t.Fatalf("no duplicates expected: %+v", gf.HiddenContests)
	}

	// Показать c2 обратно.
	code, resp = postJSON(t, h.AdminCombinedSetContestHidden, `{"slug":"combo","contest_id":"c2","hidden":false}`)
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("show failed: code=%d resp=%v", code, resp)
	}
	if gf := readGroupFileRaw(t, dataDir, "combo"); len(gf.HiddenContests) != 0 {
		t.Fatalf("hidden_contests must be empty after show: %+v", gf.HiddenContests)
	}

	// Обычная группа — отказ.
	setupGroup(t, dataDir, "normal", `[]`)
	code, resp = postJSON(t, h.AdminCombinedSetContestHidden, `{"slug":"normal","contest_id":"c1","hidden":true}`)
	if code == http.StatusOK || resp["ok"] == true {
		t.Fatalf("must reject normal group: code=%d resp=%v", code, resp)
	}
}
