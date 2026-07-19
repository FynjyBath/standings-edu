package standings

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

func tAt(base time.Time, min float64) time.Time {
	return base.Add(time.Duration(min * float64(time.Minute)))
}

// Сессионизация: перерыв больше 2 часов режет сессию, паузы между занятиями не
// попадают в активное время; δ0 добавляется первой посылке сессии.
func TestBuildStudentTaskTimesSessions(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	st := newAccountStatuses()
	st.timed["a"] = []source.TimedSubmission{
		{At: tAt(base, 0)},     // сессия 1: открытие (δ0)
		{At: tAt(base, 20)},    // +20 мин на «a»
		{At: tAt(base, 24*60)}, // через сутки: сессия 2 (δ0)
	}
	st.timed["b"] = []source.TimedSubmission{
		{At: tAt(base, 50), Solved: true}, // +30 мин на «b» в сессии 1
	}
	tt := buildStudentTaskTimes(st)

	if len(tt.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(tt.sessions))
	}
	// δ0: внутрисессионные паузы 20 и 30 мин → медиана 25 → капится до 10.
	wantA := 10.0 + 20 + 10 // δ0 + 20 внутри + δ0 второй сессии
	if tt.taskMin["a"] != wantA {
		t.Fatalf("T[a] = %v, want %v", tt.taskMin["a"], wantA)
	}
	if tt.taskMin["b"] != 30 {
		t.Fatalf("T[b] = %v, want 30 (сутки перерыва не должны попадать)", tt.taskMin["b"])
	}
	if tt.solvedAt["b"].IsZero() {
		t.Fatal("solvedAt[b] должен быть заполнен")
	}
}

// Веса: медиана времени решивших + сглаживание к типичной задаче.
func TestCourseWeights(t *testing.T) {
	tasks := []courseTask{{norm: "x"}, {norm: "y"}}
	statuses := map[string]*accountStatuses{}
	times := map[string]studentTaskTime{}
	// 7 учеников решили x за 10 минут; y никто не решил.
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("s%d", i)
		st := newAccountStatuses()
		st.solved["x"] = struct{}{}
		statuses[id] = st
		times[id] = studentTaskTime{taskMin: map[string]float64{"x": 10}}
	}
	w := courseWeights(tasks, times, statuses)
	// ŵ_x=10 (n=7), w̄=10 → w_x = (7*10+5*10)/12 = 10.
	if w["x"] != 10 {
		t.Fatalf("w[x] = %v, want 10", w["x"])
	}
	// y: n=0 → w̄ = 10.
	if w["y"] != 10 {
		t.Fatalf("w[y] = %v, want 10 (сглаживание к типичной)", w["y"])
	}
}

// Интеграционно: прогресс, скорость, застревание, брошенные, фронт.
func TestComputeCourseStats(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(7 * 24 * time.Hour)

	// Курс: контест K2 (внизу страницы → начало курса) задачи a,b; контест K1 — c,d.
	std := domain.GeneratedGroupStandings{
		GroupSlug: "g", GroupTitle: "Группа",
		Contests: []domain.GeneratedContestStandings{
			{Title: "K1", Tasks: []domain.GeneratedTask{{Label: "A", NormalizedURL: "c"}, {Label: "B", NormalizedURL: "d"}}},
			{Title: "K2", Tasks: []domain.GeneratedTask{{Label: "A", NormalizedURL: "a"}, {Label: "B", NormalizedURL: "b", Name: "Брошенная"}}},
		},
	}

	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	// 6 «фоновых» учеников для весов: решают каждую задачу за ~10 минут.
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("bg%d", i)
		students = append(students, domain.Student{ID: id})
		st := newAccountStatuses()
		cur := base
		for _, norm := range []string{"a", "b", "c", "d"} {
			st.timed[norm] = []source.TimedSubmission{{At: cur.Add(10 * time.Minute), Solved: true}}
			st.solved[norm] = struct{}{}
			st.attempted[norm] = struct{}{}
			cur = cur.Add(10 * time.Minute)
		}
		statuses[id] = st
	}
	// Испытуемый: решил a, c, d; над b бился 3 часа в двух сессиях и бросил.
	st := newAccountStatuses()
	st.timed["a"] = []source.TimedSubmission{{At: tAt(base, 10), Solved: true}}
	st.solved["a"] = struct{}{}
	st.attempted["a"] = struct{}{}
	// b: две сессии попыток по ~90 минут каждая (посылки каждые 30 мин), не решена.
	bSubs := []source.TimedSubmission{}
	for d := 0.0; d <= 90; d += 30 {
		bSubs = append(bSubs, source.TimedSubmission{At: tAt(base, 30+d)})
	}
	for d := 0.0; d <= 90; d += 30 {
		bSubs = append(bSubs, source.TimedSubmission{At: tAt(base, 24*60+d)})
	}
	st.timed["b"] = bSubs
	st.attempted["b"] = struct{}{}
	// c, d — решены в третьей сессии.
	st.timed["c"] = []source.TimedSubmission{{At: tAt(base, 48*60), Solved: true}}
	st.timed["d"] = []source.TimedSubmission{{At: tAt(base, 48*60+15), Solved: true}}
	st.solved["c"] = struct{}{}
	st.solved["d"] = struct{}{}
	st.attempted["c"] = struct{}{}
	st.attempted["d"] = struct{}{}
	students = append(students, domain.Student{ID: "hero"})
	statuses["hero"] = st

	stats := computeCourseStats(std, students, statuses, now, nil)
	cs := stats["hero"]
	if cs == nil {
		t.Fatal("nil stats")
	}
	if cs.TotalCount != 4 || cs.SolvedCount != 3 {
		t.Fatalf("solved/total = %d/%d, want 3/4", cs.SolvedCount, cs.TotalCount)
	}
	if cs.Progress <= 0.5 || cs.Progress >= 1 {
		t.Fatalf("progress = %v, want (0.5,1)", cs.Progress)
	}
	// Фронт — последняя решённая по курсу: курс = [a b c d] → d = «K1 · B».
	if cs.Front != "K1 · B" {
		t.Fatalf("front = %q, want K1 · B", cs.Front)
	}
	// Брошенная b: после неё решены c и d (≥2), сама с попытками и не решена.
	if len(cs.Abandoned) != 1 || cs.Abandoned[0].Name != "Брошенная" {
		t.Fatalf("abandoned = %+v", cs.Abandoned)
	}
	// Она же и «застрял»: ~3 часа при типичных ~10 минутах (ratio ≫ 3).
	if len(cs.Stuck) != 1 || cs.Stuck[0].Ratio < courseStuckRatio {
		t.Fatalf("stuck = %+v", cs.Stuck)
	}
	// Скорость: LowData=false у героя? активного времени ~3.5 ч, решено 3 (<5) → LowData.
	if !cs.LowData {
		t.Fatalf("hero должен быть low-data (решено 3 < %d)", courseMinSolved)
	}
	// Фоновый ученик: 4 решённые — всё ещё меньше порога 5, но активного времени мало.
	bg := stats["bg0"]
	if bg.SolvedCount != 4 || bg.Progress != 1 {
		t.Fatalf("bg: %+v", bg)
	}
}

// Скорость и «форма»: у ровного ученика с достаточными данными v ≈ 1.
func TestComputeCourseStatsSpeedCalibration(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(14 * 24 * time.Hour)

	// Курс из 8 задач одним контестом (порядок внутри контеста слева направо).
	tasks := make([]domain.GeneratedTask, 8)
	norms := make([]string, 8)
	for i := range tasks {
		norms[i] = fmt.Sprintf("t%d", i)
		tasks[i] = domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norms[i]}
	}
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}}}

	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	// 9 одинаковых учеников: решают все 8 задач по 30 минут, две сессии по 4 задачи.
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("s%d", i)
		students = append(students, domain.Student{ID: id})
		st := newAccountStatuses()
		for j, norm := range norms {
			day := j / 4 // две сессии в разные дни
			min := float64(30 * (j%4 + 1))
			at := base.Add(time.Duration(day) * 72 * time.Hour).Add(time.Duration(min) * time.Minute)
			st.timed[norm] = []source.TimedSubmission{{At: at, Solved: true}}
			st.solved[norm] = struct{}{}
			st.attempted[norm] = struct{}{}
		}
		statuses[id] = st
	}
	stats := computeCourseStats(std, students, statuses, now, nil)
	cs := stats["s0"]
	if cs.LowData {
		t.Fatalf("данных достаточно: %+v", cs)
	}
	// После нормировки на медиану когорты у медианного (здесь — любого из
	// одинаковых) ученика скорость ровно ×1.
	if cs.Speed != 1 {
		t.Fatalf("калибровка: у медианного ученика v=1, got %v", cs.Speed)
	}
	if cs.SpeedRecent <= 0 {
		t.Fatalf("speed_recent должен посчитаться: %+v", cs)
	}
	if cs.Progress != 1 || cs.ForecastWeeks != 0 {
		t.Fatalf("курс пройден: progress=%v forecast=%v", cs.Progress, cs.ForecastWeeks)
	}
}

// newCheaterCohort — общая фикстура детекторов: курс из 10 задач одним
// контестом, 8 «нормальных» учеников (решают каждую задачу за ~20 минут со
// второй попытки — first-try rate 0, все задачи «нелёгкие») и «читер» (первые
// 6 задач first-try пачкой с паузами по 2 минуты).
func newCheaterCohort(base time.Time) (domain.GeneratedGroupStandings, []domain.Student, map[string]*accountStatuses, []string) {
	tasks := make([]domain.GeneratedTask, 10)
	norms := make([]string, 10)
	for i := range tasks {
		norms[i] = fmt.Sprintf("t%d", i)
		tasks[i] = domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norms[i]}
	}
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}}}

	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("s%d", i)
		students = append(students, domain.Student{ID: id})
		st := newAccountStatuses()
		cur := base
		for _, norm := range norms {
			st.timed[norm] = []source.TimedSubmission{
				{At: cur.Add(10 * time.Minute)},
				{At: cur.Add(20 * time.Minute), Solved: true},
			}
			st.solved[norm] = struct{}{}
			st.attempted[norm] = struct{}{}
			cur = cur.Add(20 * time.Minute)
		}
		statuses[id] = st
	}
	cheat := newAccountStatuses()
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(2*i) * time.Minute)
		cheat.timed[norms[i]] = []source.TimedSubmission{{At: at, Solved: true}}
		cheat.solved[norms[i]] = struct{}{}
		cheat.attempted[norms[i]] = struct{}{}
	}
	students = append(students, domain.Student{ID: "cheat"})
	statuses["cheat"] = cheat
	return std, students, statuses, norms
}

// Детекторы нечестности: серия first-try на нелёгких, «пулемёт», «резко
// быстрее»; честный ученик — без флагов.
func TestDetectCourseFlags(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(7 * 24 * time.Hour)

	std, students, statuses, _ := newCheaterCohort(base)

	stats := computeCourseStats(std, students, statuses, now, nil)
	if n := len(stats["cheat"].Flags); n == 0 {
		t.Fatalf("у читера должны быть флаги: %+v", stats["cheat"])
	}
	// Первый флаг — серия first-try (6 подряд, все нелёгкие).
	f := stats["cheat"].Flags[0]
	if !strings.Contains(f.Text, "с первой попытки") {
		t.Fatalf("ожидали флаг серии first-try: %+v", f)
	}
	if len(f.Tasks) == 0 || f.At.IsZero() {
		t.Fatalf("флаг должен нести задачи и время: %+v", f)
	}
	if f.Key == "" {
		t.Fatalf("флаг должен нести стабильный ключ для отметки «проверено»: %+v", f)
	}
	// Окно эпизода: Until — последнее событие (по нему фильтруется лента посылок).
	if f.Until.Before(f.At) || f.Until.Sub(f.At) > time.Hour {
		t.Fatalf("окно эпизода неверно: at=%v until=%v", f.At, f.Until)
	}
	// Честные ученики — без флагов.
	for i := 0; i < 8; i++ {
		if n := len(stats[fmt.Sprintf("s%d", i)].Flags); n != 0 {
			t.Fatalf("у честного s%d не должно быть флагов: %+v", i, stats[fmt.Sprintf("s%d", i)].Flags)
		}
	}
}

// Решённое БЕЗ зафиксированного времени (ACMP, исключённый эпизод флага) не
// участвует в скорости: вес без минут в знаменателе раздувал бы её. У ученика с
// теми же временами, что у когорты, плюс пачкой «безвременных» решений скорость
// должна остаться ×1, как у всех.
func TestSpeedIgnoresSolvedWithoutTime(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(14 * 24 * time.Hour)

	tasks := make([]domain.GeneratedTask, 12)
	norms := make([]string, 12)
	for i := range tasks {
		norms[i] = fmt.Sprintf("t%d", i)
		tasks[i] = domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norms[i]}
	}
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}}}

	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	// Когорта: первые 8 задач решаются по 30 минут (две сессии), с временем.
	solveTimed := func(st *accountStatuses) {
		for j := 0; j < 8; j++ {
			day := j / 4
			min := float64(30 * (j%4 + 1))
			at := base.Add(time.Duration(day) * 72 * time.Hour).Add(time.Duration(min) * time.Minute)
			st.timed[norms[j]] = []source.TimedSubmission{{At: at, Solved: true}}
			st.solved[norms[j]] = struct{}{}
			st.attempted[norms[j]] = struct{}{}
		}
	}
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("s%d", i)
		students = append(students, domain.Student{ID: id})
		st := newAccountStatuses()
		solveTimed(st)
		statuses[id] = st
	}
	// Испытуемый: те же 8 с временем + 4 решённые БЕЗ посылок с временем.
	mixed := newAccountStatuses()
	solveTimed(mixed)
	for j := 8; j < 12; j++ {
		mixed.solved[norms[j]] = struct{}{}
		mixed.attempted[norms[j]] = struct{}{}
	}
	students = append(students, domain.Student{ID: "mixed"})
	statuses["mixed"] = mixed

	stats := computeCourseStats(std, students, statuses, now, nil)
	cs := stats["mixed"]
	if cs.LowData {
		t.Fatalf("данных достаточно: %+v", cs)
	}
	// Времена как у когорты → скорость ровно ×1; «безвременные» решения дают
	// прогресс, но не скорость.
	if cs.Speed != 1 {
		t.Fatalf("безвременные решения не должны раздувать скорость: ×%v, want ×1", cs.Speed)
	}
	if cs.SolvedCount != 12 || cs.Progress <= stats["s0"].Progress {
		t.Fatalf("прогресс должен учитывать все решённые: %+v", cs)
	}
}

// Исходы проверки и подсчёт темпа: «перенос» и «нарушение» исключают посылки
// эпизода (активное время падает, флаг не детектируется заново, прогресс цел),
// «сам решил» и старые записи без исхода не исключают ничего.
func TestComputeCourseStatsExcludesReviewedEpisodes(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(7 * 24 * time.Hour)

	std, students, statuses, _ := newCheaterCohort(base)

	// Базлайн — без отметок: флаг детектируется, но его эпизод по умолчанию
	// УЖЕ исключён из темпа (неразмеченному не доверяем).
	noRev := computeCourseStats(std, students, statuses, now, nil)
	if len(noRev["cheat"].Flags) == 0 {
		t.Fatal("прекондиция: без отметок у читера должны быть флаги")
	}
	flag := noRev["cheat"].Flags[0]
	if len(flag.TaskURLs) == 0 {
		t.Fatalf("флаг должен нести TaskURLs для исключения: %+v", flag)
	}
	if len(statuses["cheat"].timed) != 6 {
		t.Fatalf("исходные statuses не должны мутироваться: %d", len(statuses["cheat"].timed))
	}

	run := func(resolution string) map[string]*domain.StudentCourseStats {
		snap := flag
		return computeCourseStats(std, students, statuses, now, domain.IndexFlagReviews(map[string]domain.FlagReview{
			domain.FlagReviewKey("cheat", flag.Key): {At: now, Resolution: resolution, Flag: &snap},
		}))
	}
	hasFlag := func(cs *domain.StudentCourseStats) bool {
		for _, f := range cs.Flags {
			if f.Key == flag.Key {
				return true
			}
		}
		return false
	}

	// «Сам решил» (и старые записи без исхода) возвращает эпизод в подсчёт:
	// времени становится БОЛЬШЕ, чем в базлайне, флаг детектируется по-прежнему.
	legit := run(domain.FlagResolutionLegit)
	if !hasFlag(legit["cheat"]) {
		t.Fatalf("при «сам решил» флаг должен детектироваться: %+v", legit["cheat"].Flags)
	}
	if legit["cheat"].ActiveHours <= noRev["cheat"].ActiveHours {
		t.Fatalf("«сам решил» должен вернуть время эпизода: %v <= %v", legit["cheat"].ActiveHours, noRev["cheat"].ActiveHours)
	}
	if old := run(""); old["cheat"].ActiveHours != legit["cheat"].ActiveHours {
		t.Fatalf("старая запись без исхода = «сам решил»: %v != %v", old["cheat"].ActiveHours, legit["cheat"].ActiveHours)
	}

	// «Перенос» и «нарушение»: эпизод исключён (как и до разметки), а флаг
	// больше не детектируется (посылки убраны до детекта — покажется из снапшота).
	for _, resolution := range []string{domain.FlagResolutionTransfer, domain.FlagResolutionViolation} {
		t.Run(resolution, func(t *testing.T) {
			after := run(resolution)
			cs := after["cheat"]
			if hasFlag(cs) {
				t.Fatalf("исключённый эпизод не должен флаговаться снова: %+v", cs.Flags)
			}
			if cs.ActiveHours != noRev["cheat"].ActiveHours {
				t.Fatalf("время как в базлайне (эпизод исключён): %v != %v", cs.ActiveHours, noRev["cheat"].ActiveHours)
			}
			if cs.SolvedCount != noRev["cheat"].SolvedCount || cs.SolvedCount != legit["cheat"].SolvedCount {
				t.Fatalf("прогресс не должен зависеть от разметки: %d", cs.SolvedCount)
			}
			// Честные ученики не затронуты ни одним из режимов.
			if after["s0"].ActiveHours != noRev["s0"].ActiveHours || after["s0"].ActiveHours != legit["s0"].ActiveHours {
				t.Fatalf("честный ученик не должен меняться: %+v", after["s0"])
			}
		})
	}
}

// Флаги не забываются: старый эпизод («читер», генерация спустя год) всё равно
// детектируется — преподаватель разбирает его сам.
func TestDetectCourseFlagsOldEpisodesKept(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)

	std, students, statuses, _ := newCheaterCohort(base)

	// И через неделю, и спустя год флаги на месте (и с тем же стабильным ключом).
	fresh := computeCourseStats(std, students, statuses, base.Add(7*24*time.Hour), nil)
	if len(fresh["cheat"].Flags) == 0 {
		t.Fatalf("свежий эпизод должен давать флаги")
	}
	old := computeCourseStats(std, students, statuses, base.Add(365*24*time.Hour), nil)
	if len(old["cheat"].Flags) == 0 {
		t.Fatalf("старый эпизод тоже должен давать флаги (не забываем): %+v", old["cheat"])
	}
	if fresh["cheat"].Flags[0].Key != old["cheat"].Flags[0].Key {
		t.Fatalf("ключ флага должен быть стабилен во времени: %q != %q",
			fresh["cheat"].Flags[0].Key, old["cheat"].Flags[0].Key)
	}
}

// Перерыв больше courseStreakMaxGapDays рвёт серию first-try: «серия»,
// растянутая через месяцы, — стиль решения, а не эпизод.
func TestDetectCourseFlagsStreakBreaksOnLongGap(t *testing.T) {
	base := time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(120 * 24 * time.Hour)

	std, students, statuses, norms := newCheaterCohort(base)
	// Переделываем читера: 3 first-try сразу и ещё 3 — через 30 дней. Без
	// разрыва это была бы серия из 6; с разрывом обе половины короче порога.
	cheat := newAccountStatuses()
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(2*i) * time.Minute)
		if i >= 3 {
			at = at.Add(30 * 24 * time.Hour)
		}
		cheat.timed[norms[i]] = []source.TimedSubmission{{At: at, Solved: true}}
		cheat.solved[norms[i]] = struct{}{}
		cheat.attempted[norms[i]] = struct{}{}
	}
	statuses["cheat"] = cheat

	stats := computeCourseStats(std, students, statuses, now, nil)
	for _, f := range stats["cheat"].Flags {
		if strings.Contains(f.Text, "с первой попытки") {
			t.Fatalf("серия с 30-дневным перерывом не должна флаговаться: %+v", f)
		}
	}
}

// Ключ флага стабилен при перетестировании: сдвиг времени первой решающей
// посылки (informatics переписывает вердикты) не меняет ключ — он считается от
// состава задач эпизода, и отметка преподавателя не отвязывается.
func TestFlagKeyStableAcrossRetest(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(7 * 24 * time.Hour)

	std, students, statuses, norms := newCheaterCohort(base)
	before := computeCourseStats(std, students, statuses, now, nil)
	if len(before["cheat"].Flags) == 0 {
		t.Fatal("прекондиция: у читера должны быть флаги")
	}
	key := before["cheat"].Flags[0].Key

	// «Перетестирование»: у первой задачи эпизода появилась более ранняя
	// решающая посылка — время первого решения сдвинулось.
	statuses["cheat"].timed[norms[0]] = append(statuses["cheat"].timed[norms[0]],
		source.TimedSubmission{At: base.Add(-30 * time.Minute), Solved: true})
	after := computeCourseStats(std, students, statuses, now, nil)
	if len(after["cheat"].Flags) == 0 {
		t.Fatalf("флаг должен детектироваться и после сдвига: %+v", after["cheat"])
	}
	if after["cheat"].Flags[0].Key != key {
		t.Fatalf("ключ должен быть стабилен при сдвиге времени: %q != %q", after["cheat"].Flags[0].Key, key)
	}
}

// «Сам решил» побеждает по задаче: при пересечении эпизодов задача из
// legit-эпизода не исключается из темпа чужим флагом.
func TestLegitWinsOnOverlappingEpisodes(t *testing.T) {
	students := []domain.Student{{ID: "s1"}}
	legitFlag := domain.CourseFlag{TaskURLs: []string{"t1", "t2"}}
	legitFlag.Key = domain.CourseFlagKey(legitFlag.TaskURLs)
	otherFlag := domain.CourseFlag{TaskURLs: []string{"t2", "t3", "t4"}}
	otherFlag.Key = domain.CourseFlagKey(otherFlag.TaskURLs)

	snap := legitFlag
	reviews := domain.IndexFlagReviews(map[string]domain.FlagReview{
		domain.FlagReviewKey("s1", legitFlag.Key): {Resolution: domain.FlagResolutionLegit, Flag: &snap},
	})
	flags := map[string][]domain.CourseFlag{"s1": {legitFlag, otherFlag}}

	out := unreviewedFlagExclusions(students, flags, reviews)
	if _, excluded := out["s1"]["t2"]; excluded {
		t.Fatalf("t2 защищена «сам решил» и не должна исключаться: %+v", out["s1"])
	}
	for _, norm := range []string{"t3", "t4"} {
		if _, excluded := out["s1"][norm]; !excluded {
			t.Fatalf("%s из неразмеченного эпизода должна исключаться: %+v", norm, out["s1"])
		}
	}
	if _, excluded := out["s1"]["t1"]; excluded {
		t.Fatalf("t1 из legit-эпизода не должна исключаться: %+v", out["s1"])
	}

	// То же для фазы 1: «перенос»-снапшот с пересечением не трогает legit-задачу.
	transferSnap := otherFlag
	reviews = domain.IndexFlagReviews(map[string]domain.FlagReview{
		domain.FlagReviewKey("s1", legitFlag.Key):    {Resolution: domain.FlagResolutionLegit, Flag: &snap},
		domain.FlagReviewKey("s1", transferSnap.Key): {Resolution: domain.FlagResolutionTransfer, Flag: &transferSnap},
	})
	out = reviewedExclusions(students, reviews)
	if _, excluded := out["s1"]["t2"]; excluded {
		t.Fatalf("фаза 1: t2 защищена «сам решил»: %+v", out["s1"])
	}
	if _, excluded := out["s1"]["t3"]; !excluded {
		t.Fatalf("фаза 1: t3 из «переноса» должна исключаться: %+v", out["s1"])
	}
}

// Мягкое сопоставление: отметка, сохранённая под старым ключом, привязывается к
// флагу по составу задач снапшота — legit продолжает действовать.
func TestFlagReviewSoftMatchByTasks(t *testing.T) {
	students := []domain.Student{{ID: "s1"}}
	flag := domain.CourseFlag{TaskURLs: []string{"t1", "t2", "t3"}}
	flag.Key = domain.CourseFlagKey(flag.TaskURLs)

	// Снапшот с чуть отличающимся составом (эпизод «подрос») и СТАРЫМ ключом.
	snap := domain.CourseFlag{Key: "1700000000|t1", TaskURLs: []string{"t1", "t2"}}
	reviews := domain.IndexFlagReviews(map[string]domain.FlagReview{
		domain.FlagReviewKey("s1", snap.Key): {Resolution: domain.FlagResolutionLegit, Flag: &snap},
	})
	flags := map[string][]domain.CourseFlag{"s1": {flag}}

	out := unreviewedFlagExclusions(students, flags, reviews)
	if len(out["s1"]) != 0 {
		t.Fatalf("legit по мягкому сопоставлению должен вернуть эпизод в темп: %+v", out["s1"])
	}
}

// Пачка мгновенных решений ловится и без first-try (решения со второй посылки,
// но интервалы крошечные).
func TestDetectCourseFlagsBurst(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(24 * time.Hour)
	tasks := make([]domain.GeneratedTask, 6)
	norms := make([]string, 6)
	for i := range tasks {
		norms[i] = fmt.Sprintf("b%d", i)
		tasks[i] = domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norms[i]}
	}
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}}}

	students := []domain.Student{}
	statuses := map[string]*accountStatuses{}
	// Когорта: ~20 минут на задачу, у половины first-try (rate 1.0 — лёгкие по
	// first-try, чтобы серию не ловить, а поймать именно пулемёт).
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("s%d", i)
		students = append(students, domain.Student{ID: id})
		st := newAccountStatuses()
		cur := base
		for _, norm := range norms {
			st.timed[norm] = []source.TimedSubmission{{At: cur.Add(20 * time.Minute), Solved: true}}
			st.solved[norm] = struct{}{}
			st.attempted[norm] = struct{}{}
			cur = cur.Add(20 * time.Minute)
		}
		statuses[id] = st
	}
	// Пулемётчик: НЕ first-try (по 2 посылки), но 5 решений с паузами 2 мин.
	mg := newAccountStatuses()
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(2*i) * time.Minute)
		mg.timed[norms[i]] = []source.TimedSubmission{
			{At: at.Add(-30 * time.Second)}, // быстрая неудачная
			{At: at, Solved: true},
		}
		mg.solved[norms[i]] = struct{}{}
		mg.attempted[norms[i]] = struct{}{}
	}
	students = append(students, domain.Student{ID: "mg"})
	statuses["mg"] = mg

	stats := computeCourseStats(std, students, statuses, now, nil)
	found := false
	for _, f := range stats["mg"].Flags {
		if strings.Contains(f.Text, "задач за") {
			found = true
		}
	}
	if !found {
		t.Fatalf("пулемёт должен быть пойман: %+v", stats["mg"].Flags)
	}
}

// courseDisplayWeights: типичное время (медиана активного времени решивших) в
// минутах, только для задач с ≥3 решившими с известным временем; stampTaskWeights
// проставляет его и в плоский список, и в подконтесты.
func TestCourseDisplayWeights(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	norms := []string{"t0", "t1", "t2"}
	tasks := make([]domain.GeneratedTask, 3)
	for i, n := range norms {
		tasks[i] = domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: n}
	}
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{
			Title: "K", Tasks: tasks,
			Subcontests: []domain.GeneratedSubcontest{{Title: "S", TaskCount: 3, Tasks: tasks}},
		}}}

	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	solve := func(id string, solved ...string) {
		students = append(students, domain.Student{ID: id})
		st := newAccountStatuses()
		for _, n := range solved {
			st.timed[n] = []source.TimedSubmission{{At: base, Solved: true}}
			st.solved[n] = struct{}{}
			st.attempted[n] = struct{}{}
		}
		statuses[id] = st
	}
	// t0 — 4 решивших (≥3, определён); t1 — 2 (мало); t2 — 0.
	solve("a", "t0", "t1")
	solve("b", "t0", "t1")
	solve("c", "t0")
	solve("d", "t0")

	w := courseDisplayWeights(std, students, statuses)
	// Одна посылка → активное время = δ0 = 10 мин, медиана 4 значений = 10.
	if w["t0"] != 10 {
		t.Fatalf("вес t0 должен быть 10 мин: %v", w["t0"])
	}
	if _, ok := w["t1"]; ok {
		t.Fatalf("t1 (2 решивших) не должен иметь вес: %v", w)
	}
	if _, ok := w["t2"]; ok {
		t.Fatalf("t2 (никто не решал) не должен иметь вес: %v", w)
	}

	stampTaskWeights(&std, w)
	if std.Contests[0].Tasks[0].Weight != 10 || std.Contests[0].Subcontests[0].Tasks[0].Weight != 10 {
		t.Fatalf("вес должен проставиться в плоский список и подконтест: %+v", std.Contests[0])
	}
	if std.Contests[0].Tasks[1].Weight != 0 {
		t.Fatalf("t1 без веса должен остаться 0: %v", std.Contests[0].Tasks[1].Weight)
	}
}

// Скорость считается только по времени решённых задач (время на нерешённых её
// не занижает), а «фантомно быстрые» решения (одинокая AC-посылка в сессии, где
// модель видит лишь δ0) упираются в floor α·вес → скорость не выше ×(1/α).
func TestSpeedSolvedOnlyWithFloor(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	const nTasks = 15

	tasks := make([]domain.GeneratedTask, 0, nTasks)
	norms := make([]string, 0, nTasks)
	for i := 0; i < nTasks; i++ {
		norm := fmt.Sprintf("t%02d", i)
		norms = append(norms, norm)
		tasks = append(tasks, domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norm})
	}
	std := domain.GeneratedGroupStandings{
		GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}},
	}

	// «Ровный» ученик: каждая задача — отдельная сессия из 3 посылок 0/+20/+40
	// мин → активное время задачи = δ0(10) + 20 + 20 = 50 мин. Вес задач ≈ 50.
	steady := func() *accountStatuses {
		st := newAccountStatuses()
		for i, norm := range norms {
			s0 := base.Add(time.Duration(i) * 2 * time.Hour)
			st.timed[norm] = []source.TimedSubmission{
				{At: s0}, {At: s0.Add(20 * time.Minute)}, {At: s0.Add(40 * time.Minute), Solved: true},
			}
			st.solved[norm] = struct{}{}
			st.attempted[norm] = struct{}{}
		}
		return st
	}

	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("bg%d", i)
		students = append(students, domain.Student{ID: id})
		statuses[id] = steady()
	}

	// «Утопающий»: решает как ровный, но дополнительно утопил ~3.5 часа в
	// нерешённой задаче x (посылки каждые 30 мин одной сессией).
	wasted := steady()
	xSubs := []source.TimedSubmission{}
	for d := 0.0; d <= 210; d += 30 {
		xSubs = append(xSubs, source.TimedSubmission{At: base.Add(100 * time.Hour).Add(time.Duration(d) * time.Minute)})
	}
	wasted.timed["x"] = xSubs
	wasted.attempted["x"] = struct{}{}
	students = append(students, domain.Student{ID: "wasted"})
	statuses["wasted"] = wasted
	// x — тоже задача курса (иначе её время и так бы не считалось).
	std.Contests[0].Tasks = append(std.Contests[0].Tasks, domain.GeneratedTask{Label: "X", NormalizedURL: "x"})

	// «Фантом»: каждая задача — своя сессия из двух посылок в минуту (обдумал
	// заранее, сел и сдал) → модель видит δ0 + 1 ≈ 11 мин на задачу при весе
	// ≈ 50. Вторая посылка — чтобы не словить флаг «подряд с первой попытки»
	// (тот исключил бы эпизоды из темпа — отдельный механизм).
	phantom := newAccountStatuses()
	for i, norm := range norms {
		at := base.Add(time.Duration(i) * 3 * time.Hour)
		phantom.timed[norm] = []source.TimedSubmission{{At: at}, {At: at.Add(time.Minute), Solved: true}}
		phantom.solved[norm] = struct{}{}
		phantom.attempted[norm] = struct{}{}
	}
	// Внекурсовая активность с обычными паузами, чтобы личный δ0 фантома не
	// схлопнулся в 1 мин (δ0 — медиана внутрисессионных пауз ученика).
	warm := []source.TimedSubmission{}
	for i := 0; i < 20; i++ {
		warm = append(warm, source.TimedSubmission{At: base.Add(-200 * time.Hour).Add(time.Duration(i) * 30 * time.Minute)})
	}
	phantom.timed["warmup"] = warm
	phantom.attempted["warmup"] = struct{}{}
	students = append(students, domain.Student{ID: "phantom"})
	statuses["phantom"] = phantom

	now := base.Add(30 * 24 * time.Hour)
	stats := computeCourseStats(std, students, statuses, now, nil)

	bg, ws, ph := stats["bg0"], stats["wasted"], stats["phantom"]
	if bg == nil || ws == nil || ph == nil {
		t.Fatal("nil stats")
	}
	if bg.LowData || ws.LowData || ph.LowData {
		t.Fatalf("не должно быть low-data: bg=%v ws=%v ph=%v", bg.LowData, ws.LowData, ph.LowData)
	}
	// Время в нерешённой x не занижает скорость: «утопающий» равен ровному.
	if ws.Speed != bg.Speed {
		t.Fatalf("время на нерешённых не должно менять скорость: wasted=%v bg=%v", ws.Speed, bg.Speed)
	}
	// Но активные часы у «утопающего» больше — время видно там.
	if ws.ActiveHours <= bg.ActiveHours {
		t.Fatalf("часы утопающего должны быть больше: %v vs %v", ws.ActiveHours, bg.ActiveHours)
	}
	// «Фантом» упирается в floor: скорость ≈ ×(1/α), а не 50/11 ≈ 4.5.
	want := 1.0 / courseSpeedFloorAlpha
	if ph.Speed < want-0.3 || ph.Speed > want+0.2 {
		t.Fatalf("фантом должен быть ограничен ×%.1f: got %v", want, ph.Speed)
	}
}

// Прогноз масштабируется КПД: у ученика, топящего половину времени в
// нерешённом, прогноз в ~2 раза длиннее, чем у ровного с тем же темпом решения.
func TestForecastScaledByEfficiency(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	const nTasks = 12

	tasks := make([]domain.GeneratedTask, 0, nTasks)
	norms := make([]string, 0, nTasks)
	for i := 0; i < nTasks; i++ {
		norm := fmt.Sprintf("t%02d", i)
		norms = append(norms, norm)
		tasks = append(tasks, domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norm})
	}
	std := domain.GeneratedGroupStandings{
		GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}},
	}

	// Решает первые 6 задач (по 50 мин: 3 посылки 0/20/40), по одной в неделю —
	// чтобы недельная активность посчиталась (нужно ≥2 положительных недель).
	solveHalf := func() *accountStatuses {
		st := newAccountStatuses()
		for i := 0; i < 6; i++ {
			s0 := base.Add(time.Duration(i) * 7 * 24 * time.Hour)
			st.timed[norms[i]] = []source.TimedSubmission{
				{At: s0}, {At: s0.Add(20 * time.Minute)}, {At: s0.Add(40 * time.Minute), Solved: true},
			}
			st.solved[norms[i]] = struct{}{}
			st.attempted[norms[i]] = struct{}{}
		}
		return st
	}

	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("bg%d", i)
		students = append(students, domain.Student{ID: id})
		statuses[id] = solveHalf()
	}

	// «Утопающий»: то же самое + каждую неделю ещё ~50 мин бьётся над
	// нерешаемой задачей x (той же сессией, посылки каждые 25 мин).
	drown := solveHalf()
	for i := 0; i < 6; i++ {
		s0 := base.Add(time.Duration(i) * 7 * 24 * time.Hour).Add(40 * time.Minute)
		drown.timed["x"] = append(drown.timed["x"],
			source.TimedSubmission{At: s0.Add(25 * time.Minute)},
			source.TimedSubmission{At: s0.Add(50 * time.Minute)})
	}
	drown.attempted["x"] = struct{}{}
	students = append(students, domain.Student{ID: "drown"})
	statuses["drown"] = drown
	std.Contests[0].Tasks = append(std.Contests[0].Tasks, domain.GeneratedTask{Label: "X", NormalizedURL: "x"})

	now := base.Add(6 * 7 * 24 * time.Hour)
	stats := computeCourseStats(std, students, statuses, now, nil)
	bg, dr := stats["bg0"], stats["drown"]
	if bg == nil || dr == nil || bg.LowData || dr.LowData {
		t.Fatalf("нет статов или low-data: %+v %+v", bg, dr)
	}
	if bg.ForecastWeeks <= 0 || dr.ForecastWeeks <= 0 {
		t.Fatalf("прогнозы должны посчитаться: bg=%v dr=%v", bg.ForecastWeeks, dr.ForecastWeeks)
	}
	// Скорость решения у обоих одинаковая; у «утопающего» КПД ~1/2, но и часов
	// в неделю вдвое больше — календарный прогноз должен быть ~равным (расхождение
	// только от округления WeeklyHours до 0.1 ч). Без КПД-поправки его прогноз
	// был бы вдвое короче (~3.5 против ~7 недель).
	if dr.WeeklyHours <= bg.WeeklyHours {
		t.Fatalf("у утопающего должно быть больше часов в неделю: dr=%v bg=%v", dr.WeeklyHours, bg.WeeklyHours)
	}
	if diff := dr.ForecastWeeks - bg.ForecastWeeks; diff < -0.5 || diff > 0.5 {
		t.Fatalf("прогнозы должны быть ~равны (КПД компенсирует лишние часы): dr=%v bg=%v", dr.ForecastWeeks, bg.ForecastWeeks)
	}
}

// Детектор «пачечной сдачи»: сессия с ≥4 решёнными задачами курса при медианном
// времени < 50% типичного даёт флаг; честная плотная сессия (t/w ≈ 1) — нет.
func TestDetectBatchSubmissionFlag(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	const nTasks = 10

	tasks := make([]domain.GeneratedTask, 0, nTasks)
	norms := make([]string, 0, nTasks)
	for i := 0; i < nTasks; i++ {
		norm := fmt.Sprintf("t%02d", i)
		norms = append(norms, norm)
		tasks = append(tasks, domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norm})
	}
	std := domain.GeneratedGroupStandings{
		GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}},
	}

	// Фон: решают каждую задачу за ~20 минут (2 посылки: промах + AC), отдельными
	// сессиями → вес ≈ 20, first-try rate = 0 (стрик-детектор не мешает).
	students := make([]domain.Student, 0)
	statuses := map[string]*accountStatuses{}
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("bg%d", i)
		students = append(students, domain.Student{ID: id})
		st := newAccountStatuses()
		for j, norm := range norms {
			s0 := base.Add(time.Duration(j) * 2 * time.Hour)
			st.timed[norm] = []source.TimedSubmission{
				{At: s0}, {At: s0.Add(20 * time.Minute), Solved: true},
			}
			st.solved[norm] = struct{}{}
			st.attempted[norm] = struct{}{}
		}
		statuses[id] = st
	}

	// «Пачечник»: 6 задач одной сессией, на каждую по 2 посылки (промах + AC
	// через 2 и 4 мин) — не first-try и паузы больше «пулемётных», но времени
	// ~4 мин/задачу при типичных 20.
	batch := newAccountStatuses()
	cur := base.Add(100 * time.Hour)
	for i := 0; i < 6; i++ {
		batch.timed[norms[i]] = []source.TimedSubmission{
			{At: cur.Add(4 * time.Minute)}, {At: cur.Add(8 * time.Minute), Solved: true},
		}
		batch.solved[norms[i]] = struct{}{}
		batch.attempted[norms[i]] = struct{}{}
		cur = cur.Add(8 * time.Minute)
	}
	students = append(students, domain.Student{ID: "batch"})
	statuses["batch"] = batch

	now := base.Add(30 * 24 * time.Hour)
	stats := computeCourseStats(std, students, statuses, now, nil)

	var batchFlags []domain.CourseFlag
	for _, f := range stats["batch"].Flags {
		if strings.Contains(f.Text, "одной сессией") {
			batchFlags = append(batchFlags, f)
		}
	}
	if len(batchFlags) != 1 {
		t.Fatalf("у пачечника должен быть ровно один флаг пачки: %+v", stats["batch"].Flags)
	}
	if got := len(batchFlags[0].TaskURLs); got != 6 {
		t.Fatalf("в эпизоде должно быть 6 задач: %d", got)
	}
	// Честные плотные сессии фона флаг не получают.
	for _, f := range stats["bg0"].Flags {
		if strings.Contains(f.Text, "одной сессией") {
			t.Fatalf("у честного ученика не должно быть флага пачки: %+v", f)
		}
	}
}

// Коррекция весов на редкость решения: задача, решённая 4 из 8 дошедших, дорожает
// в (8/4)^0.5 ≈ 1.41 раза; задача, решённая всеми дошедшими, — нет. Нормировка
// по дошедшим: задача в конце курса, до которой дошли немногие, не дорожает
// только из-за позиции.
func TestCourseWeightsRarityCorrection(t *testing.T) {
	tasks := []courseTask{{norm: "easy"}, {norm: "hard"}, {norm: "tail"}}
	statuses := map[string]*accountStatuses{}
	times := map[string]studentTaskTime{}
	// 8 учеников: все решили easy за 10 мин; 4 решили hard за 10 мин;
	// из них 2 решили tail за 10 мин (фронт этих двоих — tail).
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("s%d", i)
		st := newAccountStatuses()
		tm := map[string]float64{"easy": 10}
		st.solved["easy"] = struct{}{}
		if i < 4 {
			st.solved["hard"] = struct{}{}
			tm["hard"] = 10
		}
		if i < 2 {
			st.solved["tail"] = struct{}{}
			tm["tail"] = 10
		}
		statuses[id] = st
		times[id] = studentTaskTime{taskMin: tm}
	}
	w := courseWeights(tasks, times, statuses)

	// easy: решили 8 из 8 дошедших → без коррекции (сглаженный ≈ 10).
	if w["easy"] < 9.9 || w["easy"] > 10.1 {
		t.Fatalf("easy: вес должен остаться ~10: %v", w["easy"])
	}
	// hard: дошли 8 (фронт ≥ hard у решивших hard и tail... фронт «дошёл» у всех 8:
	// фронт первых четырёх — hard/tail, у остальных — easy (не дошли).
	// Дошли до hard: 4 (фронт hard) + 2 (фронт tail)... у i<4 фронт ≥ hard.
	// reached(hard) = 4? Нет: фронт s0..s3 — hard или tail (≥1), s4..s7 — easy (0).
	// reached(hard)=4, solved=4 → p=1 → без коррекции.
	if w["hard"] < 9.9 || w["hard"] > 10.1 {
		t.Fatalf("hard: все дошедшие решили → без коррекции: %v", w["hard"])
	}
	// tail: дошли 2 (фронт tail), решили 2 → p=1, но reached=2 < minReached →
	// коррекции нет (шумно).
	if w["tail"] < 9.9 || w["tail"] > 10.1 {
		t.Fatalf("tail: мало дошедших → без коррекции: %v", w["tail"])
	}

	// Теперь редкость: до hard дошли 8 (у всех фронт ≥ hard), решили 4.
	for i := 4; i < 8; i++ {
		id := fmt.Sprintf("s%d", i)
		st := statuses[id]
		st.solved["tail"] = struct{}{} // фронт сдвигается на tail — дошли до hard, но не решили её
		tm := times[id].taskMin
		tm["tail"] = 10
	}
	w = courseWeights(tasks, times, statuses)
	// hard: reached=8, solved=4 → множитель (8/4)^0.5 = √2 ≈ 1.41.
	want := 10 * math.Sqrt2
	if math.Abs(w["hard"]-want) > 0.5 {
		t.Fatalf("hard: вес должен вырасти до ~%.1f: %v", want, w["hard"])
	}
	// easy: решили все 8 дошедших — по-прежнему без коррекции.
	if w["easy"] < 9.9 || w["easy"] > 10.1 {
		t.Fatalf("easy: вес должен остаться ~10: %v", w["easy"])
	}
}
