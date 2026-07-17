package httpapi

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/storage"
)

// Отметки проверки флагов нечестности (🚩). Генерация пересчитывает флаги
// заново, поэтому отметки живут отдельно — в data/flag_reviews.json, по
// стабильному ключу флага, и накладываются на профиль при отдаче страниц.
// Исходы: «сам решил» (legit) — всё учитывается; «перенос» (transfer) и
// «нарушение» (violation) — посылки эпизода исключаются из подсчёта темпа при
// следующей генерации (по снапшоту флага в отметке), а «нарушение» вдобавок
// остаётся подсвеченным в профиле навсегда.

const flagReviewCommentMaxLen = 500

func (h *Handlers) flagReviewsPath() string {
	return filepath.Join(h.dataDir, "flag_reviews.json")
}

// loadFlagReviews — единый ридер (storage.LoadFlagReviews) с логом ошибки.
func (h *Handlers) loadFlagReviews() map[string]domain.FlagReview {
	out, err := storage.LoadFlagReviews(h.dataDir)
	if err != nil {
		h.logger.Printf("ERROR read flag reviews: %v", err)
	}
	return out
}

// loadFlagReviewIndex — отметки, проиндексированные по ученикам (один раз на запрос).
func (h *Handlers) loadFlagReviewIndex() domain.StudentFlagReviews {
	return domain.IndexFlagReviews(h.loadFlagReviews())
}

// applyFlagReviews накладывает отметки проверки на флаги курс-статов (по
// точному ключу, иначе по составу задач снапшота). Отметки глобальны по
// ученику: эпизод общего контеста, размеченный в одной группе, сереет и в
// разрезах остальных. Снапшоты «перенос»/«нарушение», которых в generated уже
// нет (их посылки исключены из темпа, и флаг не детектируется), воскрешаются
// один раз — в группе, где размечали (иначе в первой). Legit-снапшоты не
// воскрешаются: если детектор больше не видит эпизод, показывать нечего.
// Флаги не забываются: показываются все, любого возраста.
func applyFlagReviews(reviews domain.StudentFlagReviews, studentID string, stats []domain.StudentCourseStats) {
	byKey := reviews[studentID]
	if len(byKey) == 0 || len(stats) == 0 {
		return
	}
	matched := make(map[string]struct{}, len(byKey))
	for i := range stats {
		for j := range stats[i].Flags {
			f := &stats[i].Flags[j]
			if f.Key == "" {
				continue
			}
			if key, rev, ok := domain.MatchFlagReview(byKey, *f); ok {
				matched[key] = struct{}{}
				at := rev.At
				f.ReviewedAt = &at
				f.ReviewComment = rev.Comment
				f.Resolution = rev.NormalizedResolution()
			}
		}
	}
	for key, rev := range byKey {
		if rev.Flag == nil || rev.NormalizedResolution() == domain.FlagResolutionLegit {
			continue
		}
		if _, ok := matched[key]; ok {
			continue
		}
		target := 0
		for i := range stats {
			if stats[i].GroupSlug == rev.Group {
				target = i
				break
			}
		}
		f := *rev.Flag
		at := rev.At
		f.ReviewedAt = &at
		f.ReviewComment = rev.Comment
		f.Resolution = rev.NormalizedResolution()
		stats[target].Flags = append(stats[target].Flags, f)
	}
}

// setFlagReview ставит отметку с исходом resolution (пустой — снять) и
// сохраняет файл (вызывается под SerializeDataWrite). Отметка глобальна по
// ученику (ключ без группы): разметка действует во всех его группах; groupSlug
// запоминается информационно (Group). Снапшот флага берётся из сгенерированного
// профиля — по нему потом работает исключение из темпа и показ после
// исчезновения флага. Ключ нормализуется к отпечатку состава эпизода (клиент
// мог прислать ключ со старой страницы), совпавшая по составу старая запись
// обновляется, а не дублируется. Отметки не протухают: запись живёт, пока
// преподаватель не снимет её сам.
func (h *Handlers) setFlagReview(studentID, groupSlug, flagKey, resolution, comment string) error {
	reviews := h.loadFlagReviews()
	key := domain.FlagReviewKey(studentID, flagKey)
	snapshot := h.findFlagSnapshot(studentID, groupSlug, flagKey)
	// Ключ со страницы мог устареть (эпизод сменил состав/ключ) — находим
	// существующую запись по составу задач снапшота.
	if _, ok := reviews[key]; !ok && snapshot != nil {
		if matchedKey, _, ok := domain.MatchFlagReview(domain.IndexFlagReviews(reviews)[studentID], *snapshot); ok {
			key = domain.FlagReviewKey(studentID, matchedKey)
		}
	}
	if resolution == "" {
		delete(reviews, key)
	} else {
		if snapshot == nil {
			// Флага нет в текущем generated (страница устарела) — но отметка уже
			// могла быть со снапшотом: тогда меняем только исход/комментарий.
			if prev, ok := reviews[key]; ok && prev.Flag != nil {
				snapshot = prev.Flag
			} else {
				return errFlagNotFound
			}
		}
		// Нормализуем ключ по составу эпизода, старую запись не дублируем.
		if len(snapshot.TaskURLs) > 0 {
			snapshot.Key = domain.CourseFlagKey(snapshot.TaskURLs)
			delete(reviews, key)
			key = domain.FlagReviewKey(studentID, snapshot.Key)
		}
		reviews[key] = domain.FlagReview{
			At:         time.Now(),
			Comment:    strings.TrimSpace(comment),
			Resolution: resolution,
			Group:      groupSlug,
			Flag:       snapshot,
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
