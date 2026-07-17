package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

// Отметки «проверено» для флагов нечестности (🚩). Генерация пересчитывает
// флаги заново, поэтому отметки живут отдельно — в data/flag_reviews.json,
// по стабильному ключу флага (время начала эпизода + первая задача), и
// накладываются на профиль при отдаче страниц.

type flagReview struct {
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
}

// flagReviewMaxAge — отметки старше не имеют смысла (сами флаги живут 60 дней)
// и вычищаются при каждой записи, чтобы файл не рос бесконечно.
const flagReviewMaxAge = 180 * 24 * time.Hour

const flagReviewCommentMaxLen = 500

func (h *Handlers) flagReviewsPath() string {
	return filepath.Join(h.dataDir, "flag_reviews.json")
}

func flagReviewMapKey(studentID, groupSlug, flagKey string) string {
	return studentID + "|" + groupSlug + "|" + flagKey
}

func (h *Handlers) loadFlagReviews() map[string]flagReview {
	out := map[string]flagReview{}
	if h.dataDir == "" {
		return out
	}
	if err := fileutil.ReadJSON(h.flagReviewsPath(), &out); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.logger.Printf("ERROR read flag reviews: %v", err)
	}
	return out
}

// applyFlagReviews накладывает отметки «проверено» на флаги курс-статов.
func applyFlagReviews(reviews map[string]flagReview, studentID string, stats []domain.StudentCourseStats) {
	if len(reviews) == 0 {
		return
	}
	for i := range stats {
		for j := range stats[i].Flags {
			f := &stats[i].Flags[j]
			if f.Key == "" {
				continue
			}
			if rev, ok := reviews[flagReviewMapKey(studentID, stats[i].GroupSlug, f.Key)]; ok {
				at := rev.At
				f.ReviewedAt = &at
				f.ReviewComment = rev.Comment
			}
		}
	}
}

// setFlagReview ставит или снимает отметку и сохраняет файл (вызывается под
// SerializeDataWrite). Заодно вычищает устаревшие отметки.
func (h *Handlers) setFlagReview(studentID, groupSlug, flagKey string, reviewed bool, comment string) error {
	reviews := h.loadFlagReviews()
	key := flagReviewMapKey(studentID, groupSlug, flagKey)
	if reviewed {
		reviews[key] = flagReview{At: time.Now(), Comment: strings.TrimSpace(comment)}
	} else {
		delete(reviews, key)
	}
	cutoff := time.Now().Add(-flagReviewMaxAge)
	for k, rev := range reviews {
		if rev.At.Before(cutoff) {
			delete(reviews, k)
		}
	}
	return fileutil.WriteJSON(h.flagReviewsPath(), reviews, 0o644)
}

type flagReviewRequest struct {
	// Slug/Token — только для жюри-эндпоинта (группа токена = группа флага).
	Slug      string `json:"slug"`
	Token     string `json:"token"`
	StudentID string `json:"student_id"`
	GroupSlug string `json:"group_slug"`
	FlagKey   string `json:"flag_key"`
	Reviewed  bool   `json:"reviewed"`
	Comment   string `json:"comment"`
}

func (req *flagReviewRequest) normalize() bool {
	req.StudentID = strings.TrimSpace(req.StudentID)
	req.GroupSlug = strings.TrimSpace(req.GroupSlug)
	req.FlagKey = strings.TrimSpace(req.FlagKey)
	if len(req.Comment) > flagReviewCommentMaxLen {
		req.Comment = req.Comment[:flagReviewCommentMaxLen]
	}
	return domain.IsValidSlug(req.StudentID) && domain.IsValidSlug(req.GroupSlug) &&
		req.FlagKey != "" && len(req.FlagKey) <= 200
}

// AdminFlagReviewSet — отметка «проверено» из админского профиля.
func (h *Handlers) AdminFlagReviewSet(w http.ResponseWriter, r *http.Request) {
	var req flagReviewRequest
	if err := decodeAdminJSON(r, &req); err != nil || !req.normalize() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	if err := h.setFlagReview(req.StudentID, req.GroupSlug, req.FlagKey, req.Reviewed, req.Comment); err != nil {
		h.logger.Printf("ERROR save flag review: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "не удалось сохранить"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// JuryFlagReviewSet — отметка «проверено» по токену группы: только флаги
// своей группы и только участников этой группы.
func (h *Handlers) JuryFlagReviewSet(w http.ResponseWriter, r *http.Request) {
	var req flagReviewRequest
	if err := decodeAdminJSON(r, &req); err != nil || !req.normalize() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !h.juryAuthorized(slug, req.Token) {
		juryDeny(w)
		return
	}
	if req.GroupSlug != slug || !h.groupContainsStudent(slug, req.StudentID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "нет доступа к этому участнику"})
		return
	}
	if err := h.setFlagReview(req.StudentID, req.GroupSlug, req.FlagKey, req.Reviewed, req.Comment); err != nil {
		h.logger.Printf("ERROR save flag review: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "не удалось сохранить"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
