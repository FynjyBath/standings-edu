package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

// juryFlagSetup — juryTestSetup + сгенерированный профиль s1 с флагом в g1
// (снапшот для отметки берётся из него).
func juryFlagSetup(t *testing.T) (*Handlers, string) {
	t.Helper()
	h, dataDir := juryTestSetup(t)
	at := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	profile := domain.GeneratedStudentProfile{
		StudentID:  "s1",
		PublicName: "Иванов И.",
		CourseStats: []domain.StudentCourseStats{{
			GroupSlug: "g1", GroupTitle: "Группа",
			Flags: []domain.CourseFlag{
				{Key: "1700000000|t1", Text: "серия", Tasks: []string{"K · A"}, TaskURLs: []string{"t1", "t2"}, At: at},
				{Key: "1700009999|t9", Text: "пулемёт", Tasks: []string{"K · C"}, TaskURLs: []string{"t9"}, At: at},
			},
		}},
	}
	dir := filepath.Join(h.loader.OutDir, "students")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fileutil.WriteJSON(filepath.Join(dir, "s1.json"), profile, 0o644); err != nil {
		t.Fatal(err)
	}
	return h, dataDir
}

// Жюри ставит исход с комментарием; отметка накладывается на флаги; «×» снимает.
func TestJuryFlagReviewSetAndClear(t *testing.T) {
	h, dataDir := juryFlagSetup(t)

	code, resp := juryPost(t, h.JuryFlagReviewSet, map[string]any{
		"slug": "g1", "token": "tok",
		"student_id": "s1", "group_slug": "g1", "flag_key": "1700000000|t1",
		"resolution": "transfer", "comment": "перенёс с ejudge",
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set review: %d %v", code, resp)
	}
	reviews := map[string]domain.FlagReview{}
	if err := fileutil.ReadJSON(filepath.Join(dataDir, "flag_reviews.json"), &reviews); err != nil {
		t.Fatal(err)
	}
	rev, ok := reviews["s1|g1|1700000000|t1"]
	if !ok || rev.Comment != "перенёс с ejudge" || rev.Resolution != domain.FlagResolutionTransfer || rev.At.IsZero() {
		t.Fatalf("review not stored: %+v", reviews)
	}
	// Снапшот флага (для исключения из темпа) должен сохраниться из профиля.
	if rev.Flag == nil || len(rev.Flag.TaskURLs) != 2 || rev.Flag.TaskURLs[0] != "t1" {
		t.Fatalf("flag snapshot missing: %+v", rev.Flag)
	}

	// Наложение на флаги профиля: помеченный получает исход, чужой — нет.
	stats := []domain.StudentCourseStats{{
		GroupSlug: "g1",
		Flags: []domain.CourseFlag{
			{Key: "1700000000|t1", Text: "серия"},
			{Key: "1700009999|t9", Text: "пулемёт"},
		},
	}}
	applyFlagReviews(h.loadFlagReviews(), "s1", stats)
	f := stats[0].Flags[0]
	if f.ReviewedAt == nil || f.ReviewComment != "перенёс с ejudge" || f.Resolution != domain.FlagResolutionTransfer {
		t.Fatalf("review must apply to matching flag: %+v", f)
	}
	if stats[0].Flags[1].ReviewedAt != nil {
		t.Fatalf("other flag must stay open: %+v", stats[0].Flags[1])
	}
	if open := stats[0].OpenFlags(); len(open) != 1 || open[0].Key != "1700009999|t9" {
		t.Fatalf("OpenFlags must return only unreviewed: %+v", open)
	}

	// Снятие отметки удаляет запись.
	code, resp = juryPost(t, h.JuryFlagReviewSet, map[string]any{
		"slug": "g1", "token": "tok",
		"student_id": "s1", "group_slug": "g1", "flag_key": "1700000000|t1",
		"resolution": "",
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("clear review: %d %v", code, resp)
	}
	if got := h.loadFlagReviews(); len(got) != 0 {
		t.Fatalf("review must be removed: %v", got)
	}
}

// Проверенные флаги показываются из снапшота, даже когда флага в generated уже
// нет (посылки исключены из темпа → флаг не детектируется), — бессрочно: флаги
// не забываются никогда, любого возраста.
func TestFlagReviewResurrectsFromSnapshot(t *testing.T) {
	h, _ := juryFlagSetup(t)

	set := func(key, resolution string) {
		t.Helper()
		code, resp := juryPost(t, h.JuryFlagReviewSet, map[string]any{
			"slug": "g1", "token": "tok",
			"student_id": "s1", "group_slug": "g1", "flag_key": key, "resolution": resolution,
		})
		if code != http.StatusOK || resp["ok"] != true {
			t.Fatalf("set %s: %d %v", resolution, code, resp)
		}
	}
	set("1700000000|t1", "violation")
	set("1700009999|t9", "transfer")

	// Профиль после «следующей генерации»: флагов больше нет.
	stats := []domain.StudentCourseStats{{GroupSlug: "g1"}}
	applyFlagReviews(h.loadFlagReviews(), "s1", stats)
	if len(stats[0].Flags) != 2 {
		t.Fatalf("both reviewed flags must resurrect (fresh): %+v", stats[0].Flags)
	}
	for _, f := range stats[0].Flags {
		if f.ReviewedAt == nil || f.Resolution == "" {
			t.Fatalf("resurrected flag must carry review: %+v", f)
		}
	}

	// Состарим эпизоды на годы — оба всё равно показываются: не забываем ничего.
	reviews := h.loadFlagReviews()
	for k, rev := range reviews {
		rev.Flag.At = time.Now().Add(-3 * 365 * 24 * time.Hour)
		reviews[k] = rev
	}
	stats = []domain.StudentCourseStats{{GroupSlug: "g1"}}
	applyFlagReviews(reviews, "s1", stats)
	if len(stats[0].Flags) != 2 {
		t.Fatalf("reviewed flags must survive any age: %+v", stats[0].Flags)
	}
}

// Жюри не может отметить чужую группу или не своего участника; токен обязателен;
// кривой resolution — 400; несуществующий флаг — 404.
func TestJuryFlagReviewForbidden(t *testing.T) {
	h, dataDir := juryFlagSetup(t)

	cases := []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"slug": "g1", "token": "WRONG", "student_id": "s1", "group_slug": "g1", "flag_key": "k", "resolution": "legit"}, http.StatusForbidden},
		{map[string]any{"slug": "g1", "token": "tok", "student_id": "s1", "group_slug": "g2", "flag_key": "k", "resolution": "legit"}, http.StatusForbidden},
		{map[string]any{"slug": "g1", "token": "tok", "student_id": "stranger", "group_slug": "g1", "flag_key": "k", "resolution": "legit"}, http.StatusForbidden},
		{map[string]any{"slug": "g1", "token": "tok", "student_id": "s1", "group_slug": "g1", "flag_key": "1700000000|t1", "resolution": "guilty"}, http.StatusBadRequest},
		{map[string]any{"slug": "g1", "token": "tok", "student_id": "s1", "group_slug": "g1", "flag_key": "no-such-flag", "resolution": "legit"}, http.StatusNotFound},
	}
	for i, c := range cases {
		if code, _ := juryPost(t, h.JuryFlagReviewSet, c.body); code != c.want {
			t.Errorf("case %d: code=%d want %d", i, code, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "flag_reviews.json")); !os.IsNotExist(err) {
		t.Fatal("file must not be created on denied requests")
	}
}

// Админский эндпоинт пишет ту же запись; старые отметки не вычищаются никогда
// (флаги не забываются) — запись живёт, пока её не снимут вручную.
func TestAdminFlagReviewKeepsOldRecords(t *testing.T) {
	h, dataDir := juryFlagSetup(t)

	ancient := time.Now().Add(-3 * 365 * 24 * time.Hour)
	old := map[string]domain.FlagReview{
		"s1|g1|ancient-legit":     {At: ancient, Resolution: domain.FlagResolutionLegit},
		"s1|g1|ancient-violation": {At: ancient, Resolution: domain.FlagResolutionViolation},
	}
	if err := fileutil.WriteJSON(filepath.Join(dataDir, "flag_reviews.json"), old, 0o644); err != nil {
		t.Fatal(err)
	}

	code, resp := juryPost(t, h.AdminFlagReviewSet, map[string]any{
		"student_id": "s1", "group_slug": "g1", "flag_key": "1700000000|t1", "resolution": "legit", "comment": "ок",
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("admin set review: %d %v", code, resp)
	}
	got := h.loadFlagReviews()
	for _, key := range []string{"s1|g1|1700000000|t1", "s1|g1|ancient-legit", "s1|g1|ancient-violation"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("record %q must be kept: %v", key, got)
		}
	}
}
