package standings

import (
	"math"
	"sort"
	"time"

	"standings-edu/internal/domain"
)

// Очередь задач на проверку преподавателем.
//
// Проверять все задачи курса никто не станет, да и незачем: оценка по условию
// совпадает с фактом на большинстве из них. Смысл есть там, где оценщик СПОРИТ
// с наблюдаемым — это либо промах оценки, либо сигнал о самой задаче: разошлись
// решения, слабые тесты, непонятная формулировка, недостижимый лимит времени.

// taskFacts — наблюдаемое по задаче, собранное по всем группам разом.
type taskFacts struct {
	task     courseTask
	tried    int
	solved   int
	firstTry int
	attempts []float64
}

// buildTaskReview собирает очередь: задачи, где оценка расходится с фактом,
// сверху. Вес расхождения умножается на охват — задача, которую видели трое,
// не должна оттеснять ту, через которую прошла вся школа.
func buildTaskReview(tasksByNorm map[string]courseTask, statusByStudent map[string]*accountStatuses, ratings domain.TaskRatings, now time.Time) *domain.GeneratedTaskReview {
	if len(tasksByNorm) == 0 {
		return nil
	}
	facts := make(map[string]*taskFacts, len(tasksByNorm))
	for norm, task := range tasksByNorm {
		facts[norm] = &taskFacts{task: task}
	}
	for _, st := range statusByStudent {
		if st == nil {
			continue
		}
		for norm, f := range facts {
			_, tried := st.attempted[norm]
			_, solved := st.solved[norm]
			if !tried && !solved {
				continue
			}
			f.tried++
			if !solved {
				continue
			}
			f.solved++
			if k, ok := attemptsToAC(st, norm); ok && k > 0 {
				f.attempts = append(f.attempts, float64(k))
				if k == 1 {
					f.firstTry++
				}
			}
		}
	}

	out := &domain.GeneratedTaskReview{GeneratedAt: now, Total: len(facts)}
	for norm, f := range facts {
		rating, ok := ratings[norm]
		if !ok || !rating.Valid() {
			continue
		}
		out.Rated++
		if f.tried == 0 {
			continue // сравнивать не с чем: задачу ещё никто не трогал
		}
		// Идейность сверяется с долей взявших С ПЕРВОЙ ПОСЫЛКИ, а не с долей
		// решивших вообще: последняя в этом курсе равна 97% почти везде и с
		// идейностью не соотносится никак.
		factRate := float64(f.firstTry) / float64(f.tried)
		row := domain.GeneratedTaskReviewRow{
			NormalizedURL: norm, URL: f.task.url, Label: f.task.label, Name: f.task.name,
			Tried: f.tried, Solved: f.solved, FactFirstTry: round2(factRate),
			RatedSolveRate: rating.SolveRate, RatedAttempts: rating.Attempts,
			IdeaScore: rating.IdeaScore(), ImplScore: rating.ImplScore(),
			Impact: f.tried, Validated: rating.Validated(), Note: rating.Note,
		}
		// Расхождение по идейности — в логитах: это та же шкала, в которой
		// смешиваются оценка и данные, поэтому разрыв читается как «на сколько
		// оценщик промахнулся в единицах модели».
		gap := math.Abs(logit(clamp01(factRate)) - logit(clamp01(rating.SolveRate)))
		if len(f.attempts) >= courseWeightMinSolvers {
			factAttempts := median(f.attempts)
			row.FactAttempts = round1(factAttempts)
			gap += math.Abs(math.Log(math.Max(1, factAttempts)) - rating.Cost())
		}
		row.Gap = round2(gap)
		row.Harder = rating.SolveRate < factRate
		out.Rows = append(out.Rows, row)
	}
	// Порядок очереди: расхождение, взвешенное на охват.
	sort.Slice(out.Rows, func(a, b int) bool {
		wa := out.Rows[a].Gap * math.Log(1+float64(out.Rows[a].Impact))
		wb := out.Rows[b].Gap * math.Log(1+float64(out.Rows[b].Impact))
		if wa != wb {
			return wa > wb
		}
		return out.Rows[a].Label < out.Rows[b].Label
	})
	return out
}

func clamp01(p float64) float64 { return math.Max(0.01, math.Min(0.99, p)) }

func logit(p float64) float64 { return math.Log(p / (1 - p)) }
