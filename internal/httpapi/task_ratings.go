package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/storage"
)

// Очередь проверки оценок сложности задач.
//
// Оценка по условию (data/task_ratings.json) входит в цену задачи как априор и
// вытесняется данными по мере их накопления. Преподавателю показывается не весь
// список задач, а только те, где оценка СПОРИТ с наблюдаемым: там либо промах
// оценщика, либо сигнал о самой задаче — разошлись решения, слабые тесты,
// непонятная формулировка.

// adminTaskRatingsLimit — сколько строк очереди показывать. Смысл страницы в
// том, чтобы её можно было пройти за один присест.
const adminTaskRatingsLimit = 30

func (h *Handlers) taskRatingsPath() string {
	return filepath.Join(h.dataDir, "task_ratings.json")
}

// AdminTaskRatingsPageData — страница очереди проверки оценок.
type AdminTaskRatingsPageData struct {
	PageTitle string
	Footer    FooterInfo
	// Rows — задачи с наибольшим расхождением, сверху.
	Rows []domain.GeneratedTaskReviewRow
	// Rated/Total — сколько задач оценено из встреченных в таблицах.
	Rated, Total int
	// Hidden — сколько строк не поместилось в лимит.
	Hidden      int
	GeneratedAt time.Time
	// NoRatings — оценок нет вовсе: показываем, как их завести.
	NoRatings bool
}

// AdminTaskRatingsPage показывает очередь задач на проверку.
func (h *Handlers) AdminTaskRatingsPage(w http.ResponseWriter, _ *http.Request) {
	page := AdminTaskRatingsPageData{
		PageTitle: "Оценки задач",
		Footer:    h.buildFooterInfo(),
	}
	review, err := h.loader.LoadTaskReview()
	if err != nil {
		h.logger.Printf("ERROR load task review: %v", err)
	}
	page.Rated, page.Total, page.GeneratedAt = review.Rated, review.Total, review.GeneratedAt
	page.NoRatings = review.Rated == 0
	page.Rows = review.Rows
	if len(page.Rows) > adminTaskRatingsLimit {
		page.Hidden = len(page.Rows) - adminTaskRatingsLimit
		page.Rows = page.Rows[:adminTaskRatingsLimit]
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_task_ratings.html", page); err != nil {
		h.logger.Printf("ERROR render task ratings page: %v", err)
	}
}

// AdminTaskRatingValidate отмечает оценку задачи проверенной — с правкой чисел,
// если преподаватель их поправил. Подтверждённой оценке модель доверяет вдвое
// сильнее (courseRatingWeightValidated), поэтому отметка — не косметика.
func (h *Handlers) AdminTaskRatingValidate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
		// Правка приходит в баллах 1..10 — в том виде, в каком её вводит
		// человек; в наблюдаемые величины переводит домен.
		IdeaScore *float64 `json:"idea_score"`
		ImplScore *float64 `json:"impl_score"`
		By        string   `json:"by"`
		Note      string   `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "не разобрать запрос"})
		return
	}
	norm := domain.NormalizeTaskURL(strings.TrimSpace(req.URL))
	if norm == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "не указана задача"})
		return
	}
	by := strings.TrimSpace(req.By)
	if by == "" {
		by = "преподаватель"
	}

	ratings, err := storage.LoadTaskRatings(h.dataDir)
	if err != nil {
		h.logger.Printf("ERROR load task ratings: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "не прочитать оценки"})
		return
	}
	rating, ok := ratings[norm]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "для этой задачи нет оценки"})
		return
	}
	if req.IdeaScore != nil {
		if *req.IdeaScore < 1 || *req.IdeaScore > 10 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "идейность — балл от 1 до 10"})
			return
		}
		rating.SolveRate = domain.IdeaFromScore(*req.IdeaScore)
	}
	if req.ImplScore != nil {
		if *req.ImplScore < 1 || *req.ImplScore > 10 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "сложность реализации — балл от 1 до 10"})
			return
		}
		rating.Attempts = domain.ImplFromScore(*req.ImplScore)
	}
	if !rating.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "оценка вне допустимых границ"})
		return
	}
	now := time.Now().UTC()
	rating.ValidatedBy = by
	rating.ValidatedAt = &now
	rating.Note = strings.TrimSpace(req.Note)
	ratings[norm] = rating

	if err := fileutil.WriteJSON(h.taskRatingsPath(), ratings, 0o644); err != nil {
		h.logger.Printf("ERROR write task ratings: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "не сохранить оценки"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"note": "оценка подтверждена; в ценах задач это отразится после генерации",
	})
}
