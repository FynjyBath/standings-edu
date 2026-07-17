package standings

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// Темп прохождения курса: сессии по посылкам, эмпирические веса задач,
// взвешенная скорость. Модель и параметры — docs/course_speed.pdf.
const (
	courseSessionGapMin = 45.0  // τ: разрыв сессии, минут
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

// computeCourseStats считает темп курса для всех учеников группы. Эпизодам с
// флагами нечестности по умолчанию НЕ доверяем: их посылки исключаются из всей
// математики темпа (времена, скорости, веса когорты), пока преподаватель не
// разметит флаг. «Сам решил» возвращает эпизод в подсчёт; «перенос»/«нарушение»
// оставляют исключённым навсегда (по снапшоту в отметке — reviews, ключ
// FlagReviewKey). Решённые задачи при этом остаются решёнными (прогресс не
// страдает), просто без времени, как задачи ACMP.
//
// Двухфазная схема: фаза 1 — данные без «перенос»/«нарушение»-эпизодов, на них
// детектируются флаги (они и показываются: неразмеченные и «сам решил»
// детектируются заново с теми же ключами); фаза 2 — из данных дополнительно
// убираются эпизоды флагов без отметки «сам решил», и темп считается по ним.
func computeCourseStats(std domain.GeneratedGroupStandings, students []domain.Student, statusByStudent map[string]*accountStatuses, now time.Time, reviews map[string]domain.FlagReview) map[string]*domain.StudentCourseStats {
	tasks := courseTasksFromStandings(std)
	if len(tasks) == 0 {
		return nil
	}

	// Фаза 1: без эпизодов, размеченных как «перенос»/«нарушение».
	statusByStudent = applyEpisodeExclusions(students, statusByStudent, reviewedExclusions(std.GroupSlug, students, reviews))

	times := make(map[string]studentTaskTime, len(students))
	for _, s := range students {
		st := statusByStudent[s.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		times[s.ID] = buildStudentTaskTimes(st)
	}
	weights := courseWeights(tasks, times, statusByStudent)
	ftRate, ftN := cohortFirstTryRates(tasks, times, statusByStudent)

	flagsByStudent := make(map[string][]domain.CourseFlag, len(students))
	for _, s := range students {
		st := statusByStudent[s.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		flagsByStudent[s.ID] = detectCourseFlags(tasks, weights, times[s.ID], st, ftRate, ftN)
	}

	// Фаза 2: дополнительно без эпизодов флагов, не размеченных «сам решил».
	if extra := unreviewedFlagExclusions(std.GroupSlug, students, flagsByStudent, reviews); len(extra) > 0 {
		statusByStudent = applyEpisodeExclusions(students, statusByStudent, extra)
		for id := range extra {
			st := statusByStudent[id]
			if st == nil {
				st = newAccountStatuses()
			}
			times[id] = buildStudentTaskTimes(st)
		}
		weights = courseWeights(tasks, times, statusByStudent)
	}

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
		cs := computeStudentCourseStats(std, tasks, weights, totalWeight, times[s.ID], st, now)
		cs.Flags = flagsByStudent[s.ID]
		out[s.ID] = cs
	}
	normalizeCourseSpeeds(out)
	return out
}

// reviewedExclusions — задачи эпизодов, размеченных «перенос»/«нарушение»
// (по снапшотам флагов в отметках), по ученикам.
func reviewedExclusions(groupSlug string, students []domain.Student, reviews map[string]domain.FlagReview) map[string]map[string]struct{} {
	if len(reviews) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{})
	for _, s := range students {
		prefix := domain.FlagReviewKey(s.ID, groupSlug, "")
		for key, rev := range reviews {
			if !strings.HasPrefix(key, prefix) || rev.Flag == nil {
				continue
			}
			if !domain.FlagResolutionExcludesTempo(rev.NormalizedResolution()) {
				continue
			}
			for _, norm := range rev.Flag.TaskURLs {
				if out[s.ID] == nil {
					out[s.ID] = make(map[string]struct{})
				}
				out[s.ID][norm] = struct{}{}
			}
		}
	}
	return out
}

// unreviewedFlagExclusions — задачи эпизодов задетектированных флагов БЕЗ
// отметки «сам решил»: до разметки эпизоду не доверяем и в темпе не учитываем.
func unreviewedFlagExclusions(groupSlug string, students []domain.Student, flagsByStudent map[string][]domain.CourseFlag, reviews map[string]domain.FlagReview) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, s := range students {
		for _, f := range flagsByStudent[s.ID] {
			if rev, ok := reviews[domain.FlagReviewKey(s.ID, groupSlug, f.Key)]; ok &&
				rev.NormalizedResolution() == domain.FlagResolutionLegit {
				continue // проверено: реально сам решил — время учитывается
			}
			for _, norm := range f.TaskURLs {
				if out[s.ID] == nil {
					out[s.ID] = make(map[string]struct{})
				}
				out[s.ID][norm] = struct{}{}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyEpisodeExclusions возвращает карту статусов, где у учеников из excluded
// посылки перечисленных задач убраны из timed. Остальные множества
// (solved/attempted/оценки) разделяются с оригиналом — они только читаются.
// Исходная карта не меняется: ей пользуются и обычные таблицы.
func applyEpisodeExclusions(students []domain.Student, statusByStudent map[string]*accountStatuses, excluded map[string]map[string]struct{}) map[string]*accountStatuses {
	if len(excluded) == 0 {
		return statusByStudent
	}
	out := statusByStudent
	copied := false
	for _, s := range students {
		st := statusByStudent[s.ID]
		skip := excluded[s.ID]
		if st == nil || len(skip) == 0 {
			continue
		}
		if !copied {
			out = make(map[string]*accountStatuses, len(statusByStudent))
			for id, v := range statusByStudent {
				out[id] = v
			}
			copied = true
		}
		clean := *st
		clean.timed = make(map[string][]source.TimedSubmission, len(st.timed))
		for norm, subs := range st.timed {
			if _, drop := skip[norm]; drop {
				continue
			}
			clean.timed[norm] = subs
		}
		out[s.ID] = &clean
	}
	return out
}

// normalizeCourseSpeeds перецентрирует скорости на медиану когорты: сырая
// v = Σw/A систематически меньше 1 у всех (в знаменателе есть время на
// нерешённое, а личное время обычно правее медианы), поэтому «×1» без
// нормировки означало бы недостижимого идеального ученика. После деления на
// медиану валидных скоростей медианный ученик получает ровно ×1 — как и
// обещает подпись «от типичного темпа». Ранжирование не меняется.
func normalizeCourseSpeeds(stats map[string]*domain.StudentCourseStats) {
	valid := make([]float64, 0, len(stats))
	for _, cs := range stats {
		if cs != nil && !cs.LowData && cs.Speed > 0 {
			valid = append(valid, cs.Speed)
		}
	}
	const minCohort = 5
	m := median(valid)
	if len(valid) < minCohort || m <= 0 {
		return // маленькая когорта — оставляем сырую шкалу
	}
	for _, cs := range stats {
		if cs == nil {
			continue
		}
		if cs.Speed > 0 {
			cs.Speed = round2(cs.Speed / m)
		}
		if cs.SpeedRecent > 0 {
			cs.SpeedRecent = round2(cs.SpeedRecent / m)
		}
	}
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

// ── Признаки нечестности ─────────────────────────────────────────────────────
// Детекторы подозрительных паттернов в посылках. Это сигналы для ЛИЧНОЙ
// проверки преподавателем, не вердикт: формулировки нейтральные, с числами.
const (
	// Серия «с первой попытки»: столько подряд решённых first-try НЕЛЁГКИХ
	// задач (когортный first-try ниже courseEasyFirstTryRate) даёт флаг.
	courseFlagStreakLen     = 5
	courseEasyFirstTryRate  = 0.6
	courseFirstTryMinCohort = 5 // меньше решивших — сложность задачи неизвестна
	// «Пулемёт»: столько решений подряд с паузами не больше courseBurstGapMin
	// минут, при суммарном типичном времени от courseBurstMinWeight минут.
	courseBurstLen       = 4
	courseBurstGapMin    = 3.0
	courseBurstMinWeight = 40.0
	// «Резко быстрее типичного»: окно из courseFastWindow подряд решённых, где
	// личное время меньше courseFastShare от типичного (вес окна значим).
	courseFastWindow    = 5
	courseFastShare     = 0.1
	courseFastMinWeight = 30.0
)

// cohortFirstTryRates считает по когорте долю решивших задачу с первой посылки.
// Возвращает map[norm]p и map[norm]n (число решивших с известными посылками).
func cohortFirstTryRates(tasks []courseTask, times map[string]studentTaskTime, statusByStudent map[string]*accountStatuses) (map[string]float64, map[string]int) {
	p := make(map[string]float64, len(tasks))
	n := make(map[string]int, len(tasks))
	for _, task := range tasks {
		ft, total := 0, 0
		for sid, st := range statusByStudent {
			if st == nil {
				continue
			}
			if _, solved := st.solved[task.norm]; !solved {
				continue
			}
			first, ok := firstSubmission(st, task.norm)
			if !ok {
				continue
			}
			_ = sid
			total++
			if first.Solved {
				ft++
			}
		}
		n[task.norm] = total
		if total > 0 {
			p[task.norm] = float64(ft) / float64(total)
		}
	}
	return p, n
}

// firstSubmission — самая ранняя посылка ученика по задаче.
func firstSubmission(st *accountStatuses, norm string) (source.TimedSubmission, bool) {
	subs := st.timed[norm]
	if len(subs) == 0 {
		return source.TimedSubmission{}, false
	}
	first := subs[0]
	for _, s := range subs[1:] {
		if s.At.Before(first.At) {
			first = s
		}
	}
	return first, true
}

// solvedCourseEvent — решённая задача курса в хронологии ученика.
type solvedCourseEvent struct {
	task     courseTask
	at       time.Time
	firstTry bool
}

// detectCourseFlags ищет подозрительные эпизоды в решениях задач курса — за всю
// историю, без окна давности: флаги не забываются, преподаватель разбирает их
// сам (проверенные сереют/подсвечиваются, но остаются).
func detectCourseFlags(tasks []courseTask, weights map[string]float64, tt studentTaskTime, st *accountStatuses, ftRate map[string]float64, ftN map[string]int) []domain.CourseFlag {
	// Хронология решений задач курса.
	events := make([]solvedCourseEvent, 0)
	for _, task := range tasks {
		at, solved := tt.solvedAt[task.norm]
		if !solved {
			continue
		}
		first, ok := firstSubmission(st, task.norm)
		events = append(events, solvedCourseEvent{task: task, at: at, firstTry: ok && first.Solved})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	if len(events) == 0 {
		return nil
	}

	flags := make([]domain.CourseFlag, 0)
	used := make(map[string]struct{}) // задачи, уже вошедшие в какой-то флаг

	appendFlag := func(text string, evs []solvedCourseEvent) {
		f := domain.CourseFlag{
			Key:  fmt.Sprintf("%d|%s", evs[0].at.Unix(), evs[0].task.norm),
			Text: text,
			At:   evs[0].at,
		}
		for _, e := range evs {
			used[e.task.norm] = struct{}{}
			if len(f.Tasks) < 6 {
				f.Tasks = append(f.Tasks, e.task.label)
			}
			// TaskURLs — без ограничения: по ним эпизод исключается из темпа.
			f.TaskURLs = append(f.TaskURLs, e.task.norm)
		}
		flags = append(flags, f)
	}
	overlaps := func(evs []solvedCourseEvent) bool {
		hit := 0
		for _, e := range evs {
			if _, ok := used[e.task.norm]; ok {
				hit++
			}
		}
		return hit*2 >= len(evs) // больше половины уже покрыто другим флагом
	}

	// 1. Серия «с первой попытки» на нелёгких задачах: непрерывная по хронологии
	// цепочка first-try решений, среди которых достаточно нелёгких.
	streak := make([]solvedCourseEvent, 0)
	hard := 0
	flushStreak := func() {
		if hard >= courseFlagStreakLen && !overlaps(streak) {
			appendFlag(fmt.Sprintf("%d задач подряд с первой попытки (из них %d — где это редкость)", len(streak), hard), streak)
		}
		streak = streak[:0]
		hard = 0
	}
	for _, e := range events {
		if !e.firstTry {
			flushStreak()
			continue
		}
		streak = append(streak, e)
		if n := ftN[e.task.norm]; n >= courseFirstTryMinCohort && ftRate[e.task.norm] < courseEasyFirstTryRate {
			hard++
		}
	}
	flushStreak()

	// 2. «Пулемёт»: подряд решённые с крошечными паузами при значимом типичном
	// времени — похоже на вставку готовых решений.
	i := 0
	for i < len(events) {
		j := i
		for j+1 < len(events) && events[j+1].at.Sub(events[j].at).Minutes() <= courseBurstGapMin {
			j++
		}
		if run := events[i : j+1]; len(run) >= courseBurstLen {
			wsum := 0.0
			for _, e := range run {
				wsum += weights[e.task.norm]
			}
			if wsum >= courseBurstMinWeight && !overlaps(run) {
				span := run[len(run)-1].at.Sub(run[0].at).Minutes()
				appendFlag(fmt.Sprintf("%d задач за %.0f мин (обычно на них уходит ~%.0f мин)", len(run), span, wsum), run)
			}
		}
		i = j + 1
	}

	// 3. Резко быстрее типичного: окно подряд решённых, где личного активного
	// времени меньше десятой доли типичного.
	for i := 0; i+courseFastWindow <= len(events); i++ {
		win := events[i : i+courseFastWindow]
		tsum, wsum := 0.0, 0.0
		for _, e := range win {
			tsum += tt.taskMin[e.task.norm]
			wsum += weights[e.task.norm]
		}
		if wsum >= courseFastMinWeight && tsum < courseFastShare*wsum && !overlaps(win) {
			appendFlag(fmt.Sprintf("%d задач при ~%.0f мин работы (обычно ~%.0f мин)", len(win), tsum, wsum), win)
		}
	}

	return flags
}
