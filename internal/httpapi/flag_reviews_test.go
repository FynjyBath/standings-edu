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

// Жюри ставит и снимает отметку «проверено»; отметка накладывается на флаги.
func TestJuryFlagReviewSetAndClear(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	code, resp := juryPost(t, h.JuryFlagReviewSet, map[string]any{
		"slug": "g1", "token": "tok",
		"student_id": "s1", "group_slug": "g1", "flag_key": "1700000000|t1",
		"reviewed": true, "comment": "разобрали на занятии",
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("set review: %d %v", code, resp)
	}
	reviews := map[string]flagReview{}
	if err := fileutil.ReadJSON(filepath.Join(dataDir, "flag_reviews.json"), &reviews); err != nil {
		t.Fatal(err)
	}
	rev, ok := reviews["s1|g1|1700000000|t1"]
	if !ok || rev.Comment != "разобрали на занятии" || rev.At.IsZero() {
		t.Fatalf("review not stored: %v", reviews)
	}

	// Наложение на флаги профиля: помеченный сереет, чужой — нет.
	stats := []domain.StudentCourseStats{{
		GroupSlug: "g1",
		Flags: []domain.CourseFlag{
			{Key: "1700000000|t1", Text: "серия"},
			{Key: "1700009999|t9", Text: "пулемёт"},
		},
	}}
	applyFlagReviews(h.loadFlagReviews(), "s1", stats)
	if stats[0].Flags[0].ReviewedAt == nil || stats[0].Flags[0].ReviewComment != "разобрали на занятии" {
		t.Fatalf("review must apply to matching flag: %+v", stats[0].Flags[0])
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
		"reviewed": false,
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("clear review: %d %v", code, resp)
	}
	if got := h.loadFlagReviews(); len(got) != 0 {
		t.Fatalf("review must be removed: %v", got)
	}
}

// Жюри не может отметить чужую группу или не своего участника; токен обязателен.
func TestJuryFlagReviewForbidden(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	cases := []map[string]any{
		{"slug": "g1", "token": "WRONG", "student_id": "s1", "group_slug": "g1", "flag_key": "k", "reviewed": true},
		{"slug": "g1", "token": "tok", "student_id": "s1", "group_slug": "g2", "flag_key": "k", "reviewed": true},
		{"slug": "g1", "token": "tok", "student_id": "stranger", "group_slug": "g1", "flag_key": "k", "reviewed": true},
	}
	for i, body := range cases {
		if code, _ := juryPost(t, h.JuryFlagReviewSet, body); code != http.StatusForbidden {
			t.Errorf("case %d: code=%d want 403", i, code)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "flag_reviews.json")); !os.IsNotExist(err) {
		t.Fatal("file must not be created on denied requests")
	}
}

// Админский эндпоинт пишет ту же запись; протухшие отметки вычищаются при записи.
func TestAdminFlagReviewAndPrune(t *testing.T) {
	h, dataDir := juryTestSetup(t)

	old := map[string]flagReview{
		"s1|g1|ancient": {At: time.Now().Add(-flagReviewMaxAge - time.Hour)},
	}
	if err := fileutil.WriteJSON(filepath.Join(dataDir, "flag_reviews.json"), old, 0o644); err != nil {
		t.Fatal(err)
	}

	code, resp := juryPost(t, h.AdminFlagReviewSet, map[string]any{
		"student_id": "s1", "group_slug": "g1", "flag_key": "fresh", "reviewed": true, "comment": "ок",
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("admin set review: %d %v", code, resp)
	}
	got := h.loadFlagReviews()
	if _, ok := got["s1|g1|fresh"]; !ok {
		t.Fatalf("fresh review missing: %v", got)
	}
	if _, ok := got["s1|g1|ancient"]; ok {
		t.Fatalf("ancient review must be pruned: %v", got)
	}
}
