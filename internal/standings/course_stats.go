package standings

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// Темп прохождения курса. Модель и её обоснование — docs/course_speed.pdf.
//
// Важное ограничение данных, из которого следует всё остальное: судья отдаёт
// только моменты посылок. Промежуток между посылками — это время ОТЛАДКИ, а не
// время работы над задачей: кто думает час и сдаёт с первой попытки, оставляет
// в данных ноль. Так решается почти половина задач. Поэтому цена задачи и сила
// ученика считаются по тому, что наблюдается, — по исходу попытки и числу
// попыток (course_fit.go), а не по восстановленным «минутам».
const (
	courseSessionGapMin = 45.0 // τ: разрыв сессии, минут
	courseDelta0MaxMin  = 10.0 // δ0: максимум надбавки на «вход» в сессию, минут
	courseHalfLifeDays  = 28.0 // H: полупериод забывания «текущей формы»
	courseStuckRatio    = 3.0  // z*: во столько раз больше посылок, чем обычно, — «застрял»
	courseMinSolved     = 5    // минимум решённых для показа темпа
	courseMinWeeks      = 2    // минимум активных недель для показа темпа
	courseMaxSignals    = 4    // сколько застреваний/брошенных показывать

	// Цена задачи складывается из двух осей: трудоёмкости (сколько посылок
	// уходит) и порога понимания (какая доля пробовавших её берёт). Взаимная
	// связь этих осей всего +0.44 — это разные вещи, и одна цена, собранная
	// только из первой, ставила «Улитку» вровень с рядовым упражнением.
	courseCostShare      = 0.6
	courseThresholdShare = 0.4
	courseFitIters       = 40  // итераций чередования медиан
	courseRaschIters     = 200 // итераций покоординатного Ньютона
	courseRaschLambda    = 1.0 // регуляризация: без неё «решили все» уводит порог в −∞
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

// courseModel — подогнанные по когорте величины курса.
type courseModel struct {
	price     map[string]float64 // цена задачи в «обычных задачах курса»
	threshold map[string]float64 // b_j: порог понимания, логиты
	ability   map[string]float64 // θ_i: сила ученика, логиты
	// ftThreshold — порог задачи по исходу «взял с первой попытки»; сила
	// ученика для флагов считается отдельно, без проверяемого эпизода.
	ftThreshold map[string]float64
	typAttempts map[string]float64 // типичное число посылок до зачёта
	total       float64            // сумма цен всех задач курса
}

// attemptsToAC — сколько посылок ученик потратил до первой зачтённой.
// Второе значение false, если посылок с временем нет (ACMP времени не отдаёт).
func attemptsToAC(st *accountStatuses, norm string) (int, bool) {
	subs := st.timed[norm]
	if len(subs) == 0 {
		return 0, false
	}
	ordered := append([]source.TimedSubmission(nil), subs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	for i, sub := range ordered {
		if sub.Solved {
			return i + 1, true
		}
	}
	return len(ordered), false
}

// fitCourseModel подгоняет цену задач и силу учеников по когорте.
//
// Три набора наблюдений, все — из того, что судья действительно сообщает:
//   - «взял / не взял» среди пробовавших   → порог понимания задачи;
//   - число посылок до зачёта              → трудоёмкость задачи;
//   - «взял с первой попытки»              → база для флагов.
//
// Обе двусторонние модели отделяют силу ученика от свойства задачи: если
// задачу взяли только сильные, это видно по их θ, а не выдаётся за лёгкость.
// Поэтому прежняя поправка на редкость решения больше не нужна — и она
// поправляла несуществующее: состав решивших по силе вдоль курса не меняется.
func fitCourseModel(tasks []courseTask, statusByStudent map[string]*accountStatuses) courseModel {
	solveObs := make([]binObs, 0)
	ftObs := make([]binObs, 0)
	costObs := make([]fitObs, 0)
	attemptSamples := make(map[string][]float64)

	ids := make([]string, 0, len(statusByStudent))
	for id := range statusByStudent {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		st := statusByStudent[id]
		if st == nil {
			continue
		}
		for _, task := range tasks {
			_, tried := st.attempted[task.norm]
			_, solved := st.solved[task.norm]
			if !tried && !solved {
				continue
			}
			solveObs = append(solveObs, binObs{row: id, col: task.norm, ok: solved})
			if !solved {
				continue
			}
			if k, ok := attemptsToAC(st, task.norm); ok && k > 0 {
				costObs = append(costObs, fitObs{row: id, col: task.norm, val: math.Log(float64(k))})
				attemptSamples[task.norm] = append(attemptSamples[task.norm], float64(k))
			}
			if first, ok := firstSubmission(st, task.norm); ok {
				ftObs = append(ftObs, binObs{row: id, col: task.norm, ok: first.Solved})
			}
		}
	}

	ability, threshold := raschFit(solveObs, courseRaschIters, courseRaschLambda)
	_, ftThreshold := raschFit(ftObs, courseRaschIters, courseRaschLambda)
	_, cost := twoWayMedianFit(costObs, courseFitIters)

	zCost, zThr := robustZ(cost), robustZ(threshold)
	raw := make(map[string]float64, len(tasks))
	for _, task := range tasks {
		raw[task.norm] = math.Exp(courseCostShare*zCost[task.norm] + courseThresholdShare*zThr[task.norm])
	}
	// Единица измерения — «обычная задача этого курса»: медианная задача стоит
	// 1.0. Минуты как единица здесь были бы обманом — их в данных нет.
	scale := median(mapValues(raw))
	if scale <= 0 {
		scale = 1
	}
	m := courseModel{
		price:       make(map[string]float64, len(tasks)),
		threshold:   threshold,
		ability:     ability,
		ftThreshold: ftThreshold,
		typAttempts: make(map[string]float64, len(tasks)),
	}
	for _, task := range tasks {
		p := raw[task.norm] / scale
		if p <= 0 || math.IsNaN(p) || math.IsInf(p, 0) {
			p = 1
		}
		m.price[task.norm] = p
		m.total += p
		if s := attemptSamples[task.norm]; len(s) >= courseWeightMinSolvers {
			m.typAttempts[task.norm] = median(s)
		}
	}
	return m
}

func mapValues(m map[string]float64) []float64 {
	out := make([]float64, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// courseWeightMinSolvers — минимум решивших с известным временем, чтобы вес
// (типичное время) задачи считался определённым и показывался в таблице.
const courseWeightMinSolvers = 3

// courseDisplayWeights — типичное время решения каждой задачи курса в минутах
// (медиана активного времени решивших) для показа в таблицах по токену. Без
// сглаживания к «типичной задаче»: показываем честную медиану, и только для
// задач, где решивших с известным временем не меньше courseWeightMinSolvers
// (иначе — определить нельзя). cohort — глобальная когорта (union групп с этим
// контестом), чтобы время было одинаковым во всех группах с задачей.
func courseDisplayWeights(std domain.GeneratedGroupStandings, cohort []domain.Student, statusByStudent map[string]*accountStatuses) map[string]float64 {
	tasks := courseTasksFromStandings(std)
	if len(tasks) == 0 {
		return nil
	}
	times := make(map[string]studentTaskTime, len(cohort))
	for _, s := range cohort {
		if st := statusByStudent[s.ID]; st != nil {
			times[s.ID] = buildStudentTaskTimes(st)
		}
	}
	out := make(map[string]float64)
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
		if len(samples) >= courseWeightMinSolvers {
			out[task.norm] = round1(median(samples))
		}
	}
	return out
}

// stampTaskWeights проставляет типичное время (Weight) на задачи стендинга по
// нормализованной ссылке — и в плоский список, и в подконтесты.
func stampTaskWeights(std *domain.GeneratedGroupStandings, weights map[string]float64) {
	if len(weights) == 0 {
		return
	}
	set := func(t *domain.GeneratedTask) {
		if w, ok := weights[t.NormalizedURL]; ok {
			t.Weight = w
		}
	}
	for i := range std.Contests {
		for j := range std.Contests[i].Tasks {
			set(&std.Contests[i].Tasks[j])
		}
		for j := range std.Contests[i].Subcontests {
			for k := range std.Contests[i].Subcontests[j].Tasks {
				set(&std.Contests[i].Subcontests[j].Tasks[k])
			}
		}
	}
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
func computeCourseStats(std domain.GeneratedGroupStandings, students []domain.Student, statusByStudent map[string]*accountStatuses, now time.Time, reviews domain.StudentFlagReviews) map[string]*domain.StudentCourseStats {
	tasks := courseTasksFromStandings(std)
	if len(tasks) == 0 {
		return nil
	}

	// Фаза 1: без эпизодов, размеченных как «перенос»/«нарушение». Отметки
	// глобальны по ученику: разметка в любой группе действует и здесь.
	statusByStudent = applyEpisodeExclusions(students, statusByStudent, reviewedExclusions(students, reviews))

	times := make(map[string]studentTaskTime, len(students))
	for _, s := range students {
		st := statusByStudent[s.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		times[s.ID] = buildStudentTaskTimes(st)
	}
	model := fitCourseModel(tasks, statusByStudent)

	flagsByStudent := make(map[string][]domain.CourseFlag, len(students))
	for _, s := range students {
		st := statusByStudent[s.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		flagsByStudent[s.ID] = detectCourseFlags(s.ID, tasks, model, times[s.ID], st)
	}

	// Фаза 2: дополнительно без эпизодов флагов, не размеченных «сам решил».
	if extra := unreviewedFlagExclusions(students, flagsByStudent, reviews); len(extra) > 0 {
		statusByStudent = applyEpisodeExclusions(students, statusByStudent, extra)
		for id := range extra {
			st := statusByStudent[id]
			if st == nil {
				st = newAccountStatuses()
			}
			times[id] = buildStudentTaskTimes(st)
		}
		model = fitCourseModel(tasks, statusByStudent)
	}

	out := make(map[string]*domain.StudentCourseStats, len(students))
	for _, s := range students {
		st := statusByStudent[s.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		cs := computeStudentCourseStats(std, s.ID, tasks, model, times[s.ID], st, now)
		cs.Flags = flagsByStudent[s.ID]
		out[s.ID] = cs
	}
	normalizeCourseTempo(out)
	return out
}

// addNorms добавляет задачи в per-student множество с ленивой инициализацией.
func addNorms(out map[string]map[string]struct{}, id string, norms []string) {
	for _, norm := range norms {
		if out[id] == nil {
			out[id] = make(map[string]struct{})
		}
		out[id][norm] = struct{}{}
	}
}

// legitNorms — задачи эпизодов, размеченных «сам решил»: по конкретной задаче
// разметка «сам решил» побеждает любой другой эпизод (задача остаётся в темпе).
func legitNorms(byKey map[string]domain.FlagReview) map[string]struct{} {
	var out map[string]struct{}
	for _, rev := range byKey {
		if rev.Flag == nil || rev.NormalizedResolution() != domain.FlagResolutionLegit {
			continue
		}
		for _, norm := range rev.Flag.TaskURLs {
			if out == nil {
				out = make(map[string]struct{})
			}
			out[norm] = struct{}{}
		}
	}
	return out
}

// subtractNorms убирает из per-student множества задачи из keep.
func subtractNorms(set map[string]map[string]struct{}, id string, keep map[string]struct{}) {
	for norm := range keep {
		delete(set[id], norm)
	}
	if len(set[id]) == 0 {
		delete(set, id)
	}
}

// reviewedExclusions — задачи эпизодов, размеченных «перенос»/«нарушение»
// (по снапшотам флагов в отметках), по ученикам; задачи legit-эпизодов
// не исключаются даже при пересечении.
func reviewedExclusions(students []domain.Student, reviews domain.StudentFlagReviews) map[string]map[string]struct{} {
	if len(reviews) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{})
	for _, s := range students {
		byKey := reviews[s.ID]
		for _, rev := range byKey {
			if rev.Flag == nil || !domain.FlagResolutionExcludesTempo(rev.NormalizedResolution()) {
				continue
			}
			addNorms(out, s.ID, rev.Flag.TaskURLs)
		}
		subtractNorms(out, s.ID, legitNorms(byKey))
	}
	return out
}

// unreviewedFlagExclusions — задачи эпизодов задетектированных флагов БЕЗ
// отметки «сам решил»: до разметки эпизоду не доверяем и в темпе не учитываем.
// Отметка ищется по точному ключу, иначе по составу задач снапшота (ключ мог
// смениться при сдвиге данных).
func unreviewedFlagExclusions(students []domain.Student, flagsByStudent map[string][]domain.CourseFlag, reviews domain.StudentFlagReviews) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, s := range students {
		byKey := reviews[s.ID]
		for _, f := range flagsByStudent[s.ID] {
			if _, rev, ok := domain.MatchFlagReview(byKey, f); ok &&
				rev.NormalizedResolution() == domain.FlagResolutionLegit {
				continue // проверено: реально сам решил — время учитывается
			}
			addNorms(out, s.ID, f.TaskURLs)
		}
		subtractNorms(out, s.ID, legitNorms(byKey))
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
// normalizeCourseTempo перецентрирует темп на когорту: медианный ученик
// получает ровно ×1. Маленькую когорту не трогаем — медиана по трём людям
// ничего не значит.
func normalizeCourseTempo(stats map[string]*domain.StudentCourseStats) {
	valid := make([]float64, 0, len(stats))
	for _, cs := range stats {
		if cs != nil && !cs.LowData && cs.Tempo > 0 {
			valid = append(valid, cs.Tempo)
		}
	}
	const minCohort = 5
	m := median(valid)
	if len(valid) < minCohort || m <= 0 {
		return
	}
	for _, cs := range stats {
		if cs == nil {
			continue
		}
		if cs.Tempo > 0 {
			cs.Tempo = round2(cs.Tempo / m)
		}
		if cs.TempoRecent > 0 {
			cs.TempoRecent = round2(cs.TempoRecent / m)
		}
	}
}

// isoWeek — ключ календарной недели.
func isoWeek(t time.Time) int {
	y, w := t.ISOWeek()
	return y*100 + w
}

func computeStudentCourseStats(std domain.GeneratedGroupStandings, studentID string, tasks []courseTask, m courseModel, tt studentTaskTime, st *accountStatuses, now time.Time) *domain.StudentCourseStats {
	cs := &domain.StudentCourseStats{
		GroupSlug:  std.GroupSlug,
		GroupTitle: std.GroupTitle,
		TotalCount: len(tasks),
		TotalPrice: round1(m.total),
	}
	courseSet := make(map[string]struct{}, len(tasks))
	for _, t := range tasks {
		courseSet[t.norm] = struct{}{}
	}

	solvedPrice := 0.0
	judgeMin := 0.0
	lastSolvedIdx := -1
	solvedIdxs := make([]int, 0)
	for i, t := range tasks {
		judgeMin += tt.taskMin[t.norm]
		if _, ok := st.solved[t.norm]; ok {
			solvedPrice += m.price[t.norm]
			cs.SolvedCount++
			solvedIdxs = append(solvedIdxs, i)
			if i > lastSolvedIdx {
				lastSolvedIdx = i
			}
		}
	}
	if m.total > 0 {
		cs.Progress = solvedPrice / m.total
	}
	cs.SolvedPrice = round1(solvedPrice)
	cs.JudgeHours = round1(judgeMin / 60)
	if lastSolvedIdx >= 0 {
		cs.Front = tasks[lastSolvedIdx].label
	}

	// Сила: какая доля курса ученику по плечу (шанс взять не ниже половины).
	// Это ответ на «что он может», отдельно от «сколько он делает».
	if len(m.threshold) > 0 {
		reach := 0
		for _, t := range tasks {
			if th, ok := m.threshold[t.norm]; ok && m.ability[studentID] >= th {
				reach++
			}
		}
		cs.Strength = round2(float64(reach) / float64(len(tasks)))
	}

	// Темп: сколько курса закрыто за календарную неделю продвижения.
	//
	// Знаменатель — недели, в которые взята хотя бы одна задача курса, а не все
	// недели с посылками. Разница огромна: у преподавателя, который годами
	// что-то шлёт на судью, недель с посылками 294, а недель с решённой задачей
	// 76 — по первому знаменателю прошедший весь курс оказывался в хвосте
	// группы. Неделя с одной случайной посылкой — это не неделя занятий.
	// Безуспешные усилия при этом не теряются: они видны в часах на судье и в
	// сигнале «застрял».
	activeWeeks := make(map[int]time.Time)
	solvedByWeek := make(map[int]float64)
	for norm, at := range tt.solvedAt {
		if _, ok := courseSet[norm]; !ok {
			continue
		}
		if _, ok := st.solved[norm]; !ok {
			continue
		}
		wk := isoWeek(at)
		solvedByWeek[wk] += m.price[norm]
		if prev, ok := activeWeeks[wk]; !ok || at.After(prev) {
			activeWeeks[wk] = at
		}
	}

	cs.LowData = cs.SolvedCount < courseMinSolved || len(activeWeeks) < courseMinWeeks
	tempoRaw := 0.0
	if !cs.LowData && len(activeWeeks) > 0 {
		tempoRaw = solvedPrice / float64(len(activeWeeks))
		cs.Tempo = round2(tempoRaw)

		// Текущая форма: те же недели с экспоненциальным забыванием.
		num, den := 0.0, 0.0
		for wk, last := range activeWeeks {
			gamma := math.Pow(2, -now.Sub(last).Hours()/24/courseHalfLifeDays)
			num += gamma * solvedByWeek[wk]
			den += gamma
		}
		if den > 0 {
			cs.TempoRecent = round2(num / den)
		}
	}

	// Недельная занятость на судье (медиана положительных недель за 8 недель).
	weekMin := make([]float64, 8)
	for si := range tt.sessions {
		s := &tt.sessions[si]
		wk := int(now.Sub(s.end).Hours() / 24 / 7)
		if wk < 0 || wk >= 8 {
			continue
		}
		for norm, mins := range s.quantum {
			if _, ok := courseSet[norm]; ok {
				weekMin[wk] += mins
			}
		}
	}
	positive := make([]float64, 0, 8)
	for _, mins := range weekMin {
		if mins > 0 {
			positive = append(positive, mins)
		}
	}
	if len(positive) >= 2 {
		cs.WeeklyHours = round1(median(positive) / 60)
	}

	// Прогноз: остаток курса по текущему недельному темпу. Никаких поправок на
	// КПД не нужно — темп уже измерен в неделях, а не в «продуктивных минутах».
	if recent := num2(cs.TempoRecent, tempoRaw); recent > 0 && m.total > solvedPrice {
		cs.ForecastWeeks = round1((m.total - solvedPrice) / recent)
	}

	// Сигналы. «Застрял» теперь считается в посылках: ученик долбит задачу
	// заметно больше раз, чем обычно на неё уходит, и всё ещё не взял.
	type sig struct {
		task  courseTask
		ratio float64
		att   float64
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
		k := float64(len(st.timed[t.norm]))
		if typ := m.typAttempts[t.norm]; typ > 0 && k > 0 && k/typ > courseStuckRatio {
			stuck = append(stuck, sig{task: t, ratio: k / typ, att: k, idx: i})
		}
		later := 0
		for _, si := range solvedIdxs {
			if si > i {
				later++
			}
		}
		if later >= 2 {
			abandoned = append(abandoned, sig{task: t, att: k, idx: i})
		}
	}
	sort.Slice(stuck, func(a, b int) bool { return stuck[a].ratio > stuck[b].ratio })
	sort.Slice(abandoned, func(a, b int) bool { return abandoned[a].idx < abandoned[b].idx })
	for _, sg := range trimSigs(stuck) {
		cs.Stuck = append(cs.Stuck, domain.CourseTaskSignal{
			Label: sg.task.label, Name: sg.task.name, URL: sg.task.url,
			Ratio: round1(sg.ratio), Attempts: sg.att,
		})
	}
	for _, sg := range trimSigs(abandoned) {
		cs.Abandoned = append(cs.Abandoned, domain.CourseTaskSignal{
			Label: sg.task.label, Name: sg.task.name, URL: sg.task.url, Attempts: sg.att,
		})
	}
	return cs
}

// num2 — первое положительное из двух.
func num2(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
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
// Сигнал для ЛИЧНОЙ проверки преподавателем, не вердикт.
//
// Раньше детекторов было четыре, с абсолютными порогами, и они помечали 69%
// учеников — то есть не помечали ничего. Главный из них, «пачечная сдача»,
// срабатывал на обычной учёбе: если решить четыре задачи за один присест,
// сессия делится между ними, и личное время оказывается меньше типичного ПО
// ПОСТРОЕНИЮ.
//
// Теперь вопрос один и он правильный: насколько эта серия невероятна ИМЕННО
// для этого ученика на ЭТИХ задачах. Сильный, взявший пять лёгких задач с
// первой попытки, не помечается — для него это ожидаемо. Слабый, взявший пять
// трудных, помечается.
const (
	courseFlagEpisodeMin  = 4    // минимум решений подряд с первой попытки
	courseFlagWindowHours = 6.0  // разрыв, рвущий эпизод
	courseFlagAlpha       = 0.02 // бюджет ложных срабатываний на ученика
)

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

// detectCourseFlags ищет серии решений «с первой попытки», слишком невероятные
// для этого ученика. Порог — α, делённое на число его эпизодов-кандидатов:
// у того, кто решает много, отдельная серия должна быть тем экстремальнее, чем
// больше у него было возможностей на такую серию наткнуться. Это и есть бюджет
// ложных срабатываний: на данных курса он даёт ~5% помеченных вместо 69%.
func detectCourseFlags(studentID string, tasks []courseTask, m courseModel, tt studentTaskTime, st *accountStatuses) []domain.CourseFlag {
	events := make([]solvedCourseEvent, 0)
	for _, task := range tasks {
		at, solved := tt.solvedAt[task.norm]
		if !solved {
			continue
		}
		if _, ok := st.solved[task.norm]; !ok {
			continue
		}
		first, ok := firstSubmission(st, task.norm)
		events = append(events, solvedCourseEvent{task: task, at: at, firstTry: ok && first.Solved})
	}
	if len(events) == 0 {
		return nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })

	// Эпизод — непрерывная по времени серия решений с первой попытки. Любое
	// решение не с первой попытки или разрыв больше окна серию рвут.
	episodes := make([][]solvedCourseEvent, 0)
	cur := make([]solvedCourseEvent, 0)
	flush := func() {
		if len(cur) >= courseFlagEpisodeMin {
			episodes = append(episodes, append([]solvedCourseEvent(nil), cur...))
		}
		cur = cur[:0]
	}
	for _, e := range events {
		if !e.firstTry {
			flush()
			continue
		}
		if len(cur) > 0 && e.at.Sub(cur[len(cur)-1].at).Hours() > courseFlagWindowHours {
			flush()
		}
		cur = append(cur, e)
	}
	flush()
	if len(episodes) == 0 {
		return nil
	}

	// Порог с поправкой на число проверок у этого ученика.
	logThreshold := math.Log(courseFlagAlpha / float64(len(episodes)))
	flags := make([]domain.CourseFlag, 0)
	for _, ep := range episodes {
		// Силу ученика оцениваем по ОСТАЛЬНОЙ его работе, без этого эпизода:
		// иначе подозрительная серия сама себя и объясняет — модель решает, что
		// человек просто сильный, и тем громче, чем меньше у него другой
		// истории. Проверяемая гипотеза именно такая: мог ли ЭТОТ ученик, судя
		// по всему остальному, выдать такую серию случайно.
		exclude := make(map[string]struct{}, len(ep))
		for _, e := range ep {
			exclude[e.task.norm] = struct{}{}
		}
		ability := firstTryAbilityExcluding(m, tasks, st, exclude)

		logP, expected := 0.0, 0.0
		for _, e := range ep {
			p := courseFirstTryChance(m, ability, e.task.norm)
			logP += math.Log(p)
			expected += p
		}
		if logP >= logThreshold {
			continue
		}
		f := domain.CourseFlag{At: ep[0].at, Until: ep[len(ep)-1].at}
		for _, e := range ep {
			if len(f.Tasks) < 6 {
				f.Tasks = append(f.Tasks, e.task.label)
			}
			f.TaskURLs = append(f.TaskURLs, e.task.norm)
		}
		f.Text = fmt.Sprintf("%d задач подряд с первой попытки — для этого ученика ожидалось ~%.1f (шанс 1 к %s)",
			len(ep), expected, formatOdds(math.Exp(logP)))
		f.Key = domain.CourseFlagKey(f.TaskURLs)
		flags = append(flags, f)
	}
	if len(flags) == 0 {
		return nil
	}
	return flags
}

// courseFirstTryChance — шанс взять задачу с первой попытки при данной силе.
// Задача без подгонки даёт ½: нейтрально, невероятности эпизод не наберёт.
func courseFirstTryChance(m courseModel, ability float64, norm string) float64 {
	b, ok := m.ftThreshold[norm]
	if !ok {
		return 0.5
	}
	p := 1 / (1 + math.Exp(-(ability - b)))
	return math.Max(1e-6, math.Min(1-1e-6, p))
}

// firstTryAbilityExcluding переоценивает силу ученика «с первой попытки» по его
// решениям ВНЕ переданных задач, при зафиксированных порогах задач. Одномерный
// Ньютон — считается мгновенно.
//
// Если другой истории нет вовсе, судить не по чему: берём нулевую силу, то есть
// меряем эпизод по мерке среднего ученика когорты. Это и правильно по смыслу, и
// закрывает вырожденный случай «в данных только подозрительная серия».
func firstTryAbilityExcluding(m courseModel, tasks []courseTask, st *accountStatuses, exclude map[string]struct{}) float64 {
	type obs struct {
		b  float64
		ok bool
	}
	rest := make([]obs, 0, len(tasks))
	for _, task := range tasks {
		if _, skip := exclude[task.norm]; skip {
			continue
		}
		if _, solved := st.solved[task.norm]; !solved {
			continue
		}
		b, known := m.ftThreshold[task.norm]
		if !known {
			continue
		}
		first, ok := firstSubmission(st, task.norm)
		if !ok {
			continue
		}
		rest = append(rest, obs{b: b, ok: first.Solved})
	}
	if len(rest) == 0 {
		return 0
	}
	theta := 0.0
	for it := 0; it < courseRaschIters; it++ {
		g, h := 0.0, 0.0
		for _, o := range rest {
			p := 1 / (1 + math.Exp(-(theta - o.b)))
			y := 0.0
			if o.ok {
				y = 1
			}
			g += y - p
			h += p * (1 - p)
		}
		g -= courseRaschLambda * theta
		h += courseRaschLambda
		if h < 1e-9 {
			break
		}
		theta += math.Max(-1, math.Min(1, g/h))
	}
	return theta
}

// formatOdds печатает 1/p как «40 000» — с разделителями, без хвоста.
func formatOdds(p float64) string {
	if p <= 0 {
		return "∞"
	}
	n := int64(math.Round(1 / p))
	s := strconv.FormatInt(n, 10)
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, c)
	}
	return string(out)
}
