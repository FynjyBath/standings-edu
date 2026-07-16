package standings

import (
	"math"
	"sort"
	"strings"
	"time"

	"standings-edu/internal/domain"
)

// Темп прохождения курса: сессии по посылкам, эмпирические веса задач,
// взвешенная скорость. Модель и параметры — docs/course_speed.pdf.
const (
	courseSessionGapMin = 120.0 // τ: разрыв сессии, минут (2 часа)
	courseDelta0MaxMin  = 10.0  // δ0: максимум надбавки на «вход» в сессию, минут
	courseWeightN0      = 5.0   // n0: сила сглаживания весов к типичной задаче
	courseHalfLifeDays  = 28.0  // H: полупериод забывания «текущей формы»
	courseStuckRatio    = 3.0   // z*: порог сигнала «застрял»
	courseMinActiveMin  = 120.0 // минимум активного времени для показа скорости
	courseMinSolved     = 5     // минимум решённых для показа скорости
	courseMaxSignals    = 4     // сколько застреваний/брошенных показывать
)

// courseTask — задача курса в порядке прохождения (контесты снизу вверх,
// внутри контеста — слева направо).
type courseTask struct {
	norm  string
	label string // «Контест · A»
	name  string
	url   string
}

// courseTasksFromStandings строит порядок курса из сгенерированных таблиц
// группы: последний контест на странице — начало курса.
func courseTasksFromStandings(std domain.GeneratedGroupStandings) []courseTask {
	out := make([]courseTask, 0)
	seen := make(map[string]struct{})
	for ci := len(std.Contests) - 1; ci >= 0; ci-- {
		c := std.Contests[ci]
		for _, t := range c.Tasks {
			norm := strings.TrimSpace(t.NormalizedURL)
			if norm == "" {
				continue
			}
			if _, dup := seen[norm]; dup {
				continue
			}
			seen[norm] = struct{}{}
			out = append(out, courseTask{
				norm:  norm,
				label: c.Title + " · " + t.Label,
				name:  t.Name,
				url:   t.URL,
			})
		}
	}
	return out
}

// studentTaskTime — активное время ученика по задачам + разбивка по сессиям.
type studentTaskTime struct {
	taskMin  map[string]float64 // T_ij, минут, по normalized URL
	sessions []courseSession
	solvedAt map[string]time.Time // первая решающая посылка задачи
}

type courseSession struct {
	end     time.Time
	quantum map[string]float64 // минуты по задачам в этой сессии
}

// buildStudentTaskTimes сессионизирует ВСЕ посылки ученика со временем (сессия —
// свойство ученика, не курса) и приписывает внутрисессионные паузы задачам.
func buildStudentTaskTimes(st *accountStatuses) studentTaskTime {
	type ev struct {
		at     time.Time
		norm   string
		solved bool
	}
	events := make([]ev, 0)
	for norm, subs := range st.timed {
		for _, s := range subs {
			events = append(events, ev{at: s.At, norm: norm, solved: s.Solved})
		}
	}
	res := studentTaskTime{taskMin: map[string]float64{}, solvedAt: map[string]time.Time{}}
	if len(events) == 0 {
		return res
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })

	// δ0 — медиана внутрисессионных пауз ученика, не больше 10 минут.
	gaps := make([]float64, 0, len(events))
	for i := 1; i < len(events); i++ {
		g := events[i].at.Sub(events[i-1].at).Minutes()
		if g > 0 && g <= courseSessionGapMin {
			gaps = append(gaps, g)
		}
	}
	delta0 := courseDelta0MaxMin
	if len(gaps) > 0 {
		delta0 = math.Min(median(gaps), courseDelta0MaxMin)
	}

	var cur *courseSession
	for i, e := range events {
		newSession := i == 0 || e.at.Sub(events[i-1].at).Minutes() > courseSessionGapMin
		var dt float64
		if newSession {
			res.sessions = append(res.sessions, courseSession{quantum: map[string]float64{}})
			cur = &res.sessions[len(res.sessions)-1]
			dt = delta0
		} else {
			dt = e.at.Sub(events[i-1].at).Minutes()
		}
		cur.quantum[e.norm] += dt
		cur.end = e.at
		res.taskMin[e.norm] += dt
		if e.solved {
			if _, ok := res.solvedAt[e.norm]; !ok {
				res.solvedAt[e.norm] = e.at
			}
		}
	}
	return res
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// courseWeights — эмпирические веса задач: медиана активного времени решивших,
// с байесовским сглаживанием к типичной задаче курса.
func courseWeights(tasks []courseTask, times map[string]studentTaskTime, statusByStudent map[string]*accountStatuses) map[string]float64 {
	raw := make(map[string]float64, len(tasks))   // ŵ_j
	count := make(map[string]float64, len(tasks)) // n_j
	for _, task := range tasks {
		samples := make([]float64, 0)
		for sid, tt := range times {
			st := statusByStudent[sid]
			if st == nil {
				continue
			}
			if _, solved := st.solved[task.norm]; !solved {
				continue
			}
			if t := tt.taskMin[task.norm]; t > 0 {
				samples = append(samples, t)
			}
		}
		raw[task.norm] = median(samples)
		count[task.norm] = float64(len(samples))
	}
	// w̄ — медиана ненулевых ŵ; если данных нет вовсе — условная «задача в 20 мин».
	nonzero := make([]float64, 0, len(raw))
	for _, w := range raw {
		if w > 0 {
			nonzero = append(nonzero, w)
		}
	}
	wbar := median(nonzero)
	if wbar <= 0 {
		wbar = 20
	}
	out := make(map[string]float64, len(tasks))
	for _, task := range tasks {
		n := count[task.norm]
		out[task.norm] = (n*raw[task.norm] + courseWeightN0*wbar) / (n + courseWeightN0)
	}
	return out
}

// computeCourseStats считает темп курса для всех учеников группы.
func computeCourseStats(std domain.GeneratedGroupStandings, students []domain.Student, statusByStudent map[string]*accountStatuses, now time.Time) map[string]*domain.StudentCourseStats {
	tasks := courseTasksFromStandings(std)
	if len(tasks) == 0 {
		return nil
	}

	times := make(map[string]studentTaskTime, len(students))
	for _, s := range students {
		st := statusByStudent[s.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		times[s.ID] = buildStudentTaskTimes(st)
	}
	weights := courseWeights(tasks, times, statusByStudent)

	totalWeight := 0.0
	for _, t := range tasks {
		totalWeight += weights[t.norm]
	}

	out := make(map[string]*domain.StudentCourseStats, len(students))
	for _, s := range students {
		st := statusByStudent[s.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		out[s.ID] = computeStudentCourseStats(std, tasks, weights, totalWeight, times[s.ID], st, now)
	}
	return out
}

func computeStudentCourseStats(std domain.GeneratedGroupStandings, tasks []courseTask, weights map[string]float64, totalWeight float64, tt studentTaskTime, st *accountStatuses, now time.Time) *domain.StudentCourseStats {
	cs := &domain.StudentCourseStats{
		GroupSlug:  std.GroupSlug,
		GroupTitle: std.GroupTitle,
		TotalCount: len(tasks),
	}
	courseSet := make(map[string]int, len(tasks)) // norm -> индекс в курсе
	for i, t := range tasks {
		courseSet[t.norm] = i
	}

	solvedWeight := 0.0
	activeMin := 0.0
	lastSolvedIdx := -1
	solvedIdxs := make([]int, 0)
	for i, t := range tasks {
		activeMin += tt.taskMin[t.norm]
		if _, ok := st.solved[t.norm]; ok {
			solvedWeight += weights[t.norm]
			cs.SolvedCount++
			solvedIdxs = append(solvedIdxs, i)
			if i > lastSolvedIdx {
				lastSolvedIdx = i
			}
		}
	}
	if totalWeight > 0 {
		cs.Progress = solvedWeight / totalWeight
	}
	cs.ActiveHours = round1(activeMin / 60)
	if lastSolvedIdx >= 0 {
		cs.Front = tasks[lastSolvedIdx].label
	}

	// Скорости.
	cs.LowData = activeMin < courseMinActiveMin || cs.SolvedCount < courseMinSolved
	if !cs.LowData && activeMin > 0 {
		cs.Speed = round2(solvedWeight / activeMin)

		// Текущая форма: EWMA по сессиям (вклад сессии — время на задачах курса
		// и вес задач курса, решённых в этой сессии).
		num, den := 0.0, 0.0
		for si := range tt.sessions {
			s := &tt.sessions[si]
			gamma := math.Pow(2, -now.Sub(s.end).Hours()/24/courseHalfLifeDays)
			dur := 0.0
			for norm, m := range s.quantum {
				if _, ok := courseSet[norm]; ok {
					dur += m
				}
			}
			w := 0.0
			for norm, at := range tt.solvedAt {
				if _, ok := courseSet[norm]; !ok {
					continue
				}
				if sessionContains(tt.sessions, si, at) {
					w += weights[norm]
				}
			}
			num += gamma * w
			den += gamma * dur
		}
		if den >= 60 { // минимум час «эффективного» недавнего времени
			cs.SpeedRecent = round2(num / den)
		}
	}

	// Недельная активность на курсе (медиана положительных недель за 8 недель).
	weekMin := make([]float64, 8)
	for si := range tt.sessions {
		s := &tt.sessions[si]
		age := now.Sub(s.end).Hours() / 24 / 7
		wk := int(age)
		if wk < 0 || wk >= 8 {
			continue
		}
		for norm, m := range s.quantum {
			if _, ok := courseSet[norm]; ok {
				weekMin[wk] += m
			}
		}
	}
	positive := make([]float64, 0, 8)
	for _, m := range weekMin {
		if m > 0 {
			positive = append(positive, m)
		}
	}
	if len(positive) >= 2 {
		cs.WeeklyHours = round1(median(positive) / 60)
	}

	// Прогноз до конца курса.
	if cs.SpeedRecent > 0 && cs.WeeklyHours > 0 && totalWeight > solvedWeight {
		remainMin := (totalWeight - solvedWeight) / cs.SpeedRecent
		cs.ForecastWeeks = round1(remainMin / 60 / cs.WeeklyHours)
	}

	// Сигналы: застревания и брошенные.
	type sig struct {
		task  courseTask
		ratio float64
		min   float64
		idx   int
	}
	stuck := make([]sig, 0)
	abandoned := make([]sig, 0)
	for i, t := range tasks {
		if _, solved := st.solved[t.norm]; solved {
			continue
		}
		if _, tried := st.attempted[t.norm]; !tried {
			continue
		}
		m := tt.taskMin[t.norm]
		w := weights[t.norm]
		if w > 0 && m/w > courseStuckRatio {
			stuck = append(stuck, sig{task: t, ratio: m / w, min: m, idx: i})
		}
		// Брошена: дальше по курсу решено ≥2 задач.
		later := 0
		for _, si := range solvedIdxs {
			if si > i {
				later++
			}
		}
		if later >= 2 {
			abandoned = append(abandoned, sig{task: t, min: m, idx: i})
		}
	}
	sort.Slice(stuck, func(a, b int) bool { return stuck[a].ratio > stuck[b].ratio })
	sort.Slice(abandoned, func(a, b int) bool { return abandoned[a].idx < abandoned[b].idx })
	for _, s := range trimSigs(stuck) {
		cs.Stuck = append(cs.Stuck, domain.CourseTaskSignal{
			Label: s.task.label, Name: s.task.name, URL: s.task.url,
			Ratio: round1(s.ratio), Minutes: round1(s.min),
		})
	}
	for _, s := range trimSigs(abandoned) {
		cs.Abandoned = append(cs.Abandoned, domain.CourseTaskSignal{
			Label: s.task.label, Name: s.task.name, URL: s.task.url, Minutes: round1(s.min),
		})
	}
	return cs
}

func trimSigs[T any](s []T) []T {
	if len(s) > courseMaxSignals {
		return s[:courseMaxSignals]
	}
	return s
}

// sessionContains — попал ли момент t в сессию с индексом si (по границам
// соседних сессий; сессии упорядочены по времени).
func sessionContains(sessions []courseSession, si int, t time.Time) bool {
	s := sessions[si]
	if t.After(s.end) {
		return false
	}
	if si > 0 && !t.After(sessions[si-1].end) {
		return false
	}
	return true
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }
