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

// Отметки проверки флагов нечестности (🚩). Генерация пересчитывает флаги
// заново, поэтому отметки живут отдельно — в data/flag_reviews.json, по
// стабильному ключу флага, и накладываются на профиль при отдаче страниц.
// Исходы: «сам решил» (legit) — всё учитывается; «перенос» (transfer) и
// «нарушение» (violation) — посылки эпизода исключаются из подсчёта темпа при
// следующей генерации (по снапшоту флага в отметке), а «нарушение» вдобавок
// остаётся подсвеченным в профиле навсегда.

// flagReviewMaxAge — отметки «сам решил»/«перенос» старше этого срока
// вычищаются при каждой записи (сами флаги живут 60 дней). «Нарушение» не
// вычищается никогда: это память «один раз был пойман».
const flagReviewMaxAge = 180 * 24 * time.Hour

const flagReviewCommentMaxLen = 500

func (h *Handlers) flagReviewsPath() string {
	return filepath.Join(h.dataDir, "flag_reviews.json")
}

func (h *Handlers) loadFlagReviews() map[string]domain.FlagReview {
	out := map[string]domain.FlagReview{}
	if h.dataDir == "" {
		return out
	}
	if err := fileutil.ReadJSON(h.flagReviewsPath(), &out); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.logger.Printf("ERROR read flag reviews: %v", err)
	}
	return out
}

// applyFlagReviews накладывает отметки проверки на флаги курс-статов и
// добавляет из снапшотов флаги, которых в generated уже нет: «нарушение» —
// всегда (память навсегда), «перенос» — пока эпизод не старше окна показа
// (после отметки посылки исключаются из темпа, и флаг перестаёт детектироваться).
func applyFlagReviews(reviews map[string]domain.FlagReview, studentID string, stats []domain.StudentCourseStats) {
	if len(reviews) == 0 {
		return
	}
	const maxAge = 60 * 24 * time.Hour // окно показа флагов, как в детекторе
	now := time.Now()
	for i := range stats {
		present := make(map[string]struct{}, len(stats[i].Flags))
		for j := range stats[i].Flags {
			f := &stats[i].Flags[j]
			if f.Key == "" {
				continue
			}
			present[f.Key] = struct{}{}
			if rev, ok := reviews[domain.FlagReviewKey(studentID, stats[i].GroupSlug, f.Key)]; ok {
				at := rev.At
				f.ReviewedAt = &at
				f.ReviewComment = rev.Comment
				f.Resolution = rev.NormalizedResolution()
			}
		}
		prefix := domain.FlagReviewKey(studentID, stats[i].GroupSlug, "")
		for key, rev := range reviews {
			if !strings.HasPrefix(key, prefix) || rev.Flag == nil {
				continue
			}
			if _, ok := present[rev.Flag.Key]; ok {
				continue
			}
			res := rev.NormalizedResolution()
			if res != domain.FlagResolutionViolation &&
				!(res == domain.FlagResolutionTransfer && now.Sub(rev.Flag.At) <= maxAge) {
				continue
			}
			f := *rev.Flag
			at := rev.At
			f.ReviewedAt = &at
			f.ReviewComment = rev.Comment
			f.Resolution = res
			stats[i].Flags = append(stats[i].Flags, f)
		}
	}
}

// setFlagReview ставит отметку с исходом resolution (пустой — снять) и
// сохраняет файл (вызывается под SerializeDataWrite). Снапшот флага берётся из
// сгенерированного профиля — по нему потом работает исключение из темпа и показ
// после исчезновения флага. Заодно вычищаются протухшие отметки (кроме «нарушений»).
func (h *Handlers) setFlagReview(studentID, groupSlug, flagKey, resolution, comment string) error {
	reviews := h.loadFlagReviews()
	key := domain.FlagReviewKey(studentID, groupSlug, flagKey)
	if resolution == "" {
		delete(reviews, key)
	} else {
		snapshot := h.findFlagSnapshot(studentID, groupSlug, flagKey)
		if snapshot == nil {
			// Флага нет в текущем generated (страница устарела) — но отметка уже
			// могла быть со снапшотом: тогда меняем только исход/комментарий.
			if prev, ok := reviews[key]; ok && prev.Flag != nil {
				snapshot = prev.Flag
			} else {
				return errFlagNotFound
			}
		}
		reviews[key] = domain.FlagReview{
			At:         time.Now(),
			Comment:    strings.TrimSpace(comment),
			Resolution: resolution,
			Flag:       snapshot,
		}
	}
	cutoff := time.Now().Add(-flagReviewMaxAge)
	for k, rev := range reviews {
		if rev.NormalizedResolution() != domain.FlagResolutionViolation && rev.At.Before(cutoff) {
			delete(reviews, k)
		}
	}
	return fileutil.WriteJSON(h.flagReviewsPath(), reviews, 0o644)
}

// findFlagSnapshot ищет флаг в сгенерированном профиле ученика (без отметок
// проверки — чистый снапшот детектора).
func (h *Handlers) findFlagSnapshot(studentID, groupSlug, flagKey string) *domain.CourseFlag {
	profile, err := h.loader.LoadStudentProfile(studentID)
	if err != nil {
		return nil
	}
	for i := range profile.CourseStats {
		if profile.CourseStats[i].GroupSlug != groupSlug {
			continue
		}
		for _, f := range profile.CourseStats[i].Flags {
			if f.Key == flagKey {
				snap := f
				return &snap
			}
		}
	}
	return nil
}

type flagReviewRequest struct {
	// Slug/Token — только для жюри-эндпоинта (группа токена = группа флага).
	Slug      string `json:"slug"`
	Token     string `json:"token"`
	StudentID string `json:"student_id"`
	GroupSlug string `json:"group_slug"`
	FlagKey   string `json:"flag_key"`
	// Resolution — legit | transfer | violation; пустая строка — снять отметку.
	Resolution string `json:"resolution"`
	Comment    string `json:"comment"`
}

func (req *flagReviewRequest) normalize() bool {
	req.StudentID = strings.TrimSpace(req.StudentID)
	req.GroupSlug = strings.TrimSpace(req.GroupSlug)
	req.FlagKey = strings.TrimSpace(req.FlagKey)
	req.Resolution = strings.TrimSpace(req.Resolution)
	if len(req.Comment) > flagReviewCommentMaxLen {
		req.Comment = req.Comment[:flagReviewCommentMaxLen]
	}
	switch req.Resolution {
	case "", domain.FlagResolutionLegit, domain.FlagResolutionTransfer, domain.FlagResolutionViolation:
	default:
		return false
	}
	return domain.IsValidSlug(req.StudentID) && domain.IsValidSlug(req.GroupSlug) &&
		req.FlagKey != "" && len(req.FlagKey) <= 200
}

var errFlagNotFound = errors.New("флаг не найден — обновите страницу")

func (h *Handlers) handleFlagReviewSet(w http.ResponseWriter, req flagReviewRequest) {
	if err := h.setFlagReview(req.StudentID, req.GroupSlug, req.FlagKey, req.Resolution, req.Comment); err != nil {
		if errors.Is(err, errFlagNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		h.logger.Printf("ERROR save flag review: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "не удалось сохранить"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AdminFlagReviewSet — отметка проверки флага из админского профиля.
func (h *Handlers) AdminFlagReviewSet(w http.ResponseWriter, r *http.Request) {
	var req flagReviewRequest
	if err := decodeAdminJSON(r, &req); err != nil || !req.normalize() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	h.handleFlagReviewSet(w, req)
}

// JuryFlagReviewSet — отметка проверки по токену группы: только флаги своей
// группы и только участников этой группы.
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
	h.handleFlagReviewSet(w, req)
}
