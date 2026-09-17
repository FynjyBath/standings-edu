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

	stats := computeCourseStats(std, students, statuses, now, nil, nil)
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

	stats := computeCourseStats(std, students, statuses, now, nil, nil)
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

// Исходы проверки и подсчёт темпа: «перенос» и «нарушение» исключают посылки
// эпизода (активное время падает, флаг не детектируется заново, прогресс цел),
// «сам решил» и старые записи без исхода не исключают ничего.
func TestComputeCourseStatsExcludesReviewedEpisodes(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(7 * 24 * time.Hour)

	std, students, statuses, _ := newCheaterCohort(base)

	// Базлайн — без отметок: флаг детектируется, но его эпизод по умолчанию
	// УЖЕ исключён из темпа (неразмеченному не доверяем).
	noRev := computeCourseStats(std, students, statuses, now, nil, nil)
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
		}), nil)
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
	if legit["cheat"].JudgeHours <= noRev["cheat"].JudgeHours {
		t.Fatalf("«сам решил» должен вернуть время эпизода: %v <= %v", legit["cheat"].JudgeHours, noRev["cheat"].JudgeHours)
	}
	if old := run(""); old["cheat"].JudgeHours != legit["cheat"].JudgeHours {
		t.Fatalf("старая запись без исхода = «сам решил»: %v != %v", old["cheat"].JudgeHours, legit["cheat"].JudgeHours)
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
			if cs.JudgeHours != noRev["cheat"].JudgeHours {
				t.Fatalf("время как в базлайне (эпизод исключён): %v != %v", cs.JudgeHours, noRev["cheat"].JudgeHours)
			}
			if cs.SolvedCount != noRev["cheat"].SolvedCount || cs.SolvedCount != legit["cheat"].SolvedCount {
				t.Fatalf("прогресс не должен зависеть от разметки: %d", cs.SolvedCount)
			}
			// Честные ученики не затронуты ни одним из режимов.
			if after["s0"].JudgeHours != noRev["s0"].JudgeHours || after["s0"].JudgeHours != legit["s0"].JudgeHours {
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
	fresh := computeCourseStats(std, students, statuses, base.Add(7*24*time.Hour), nil, nil)
	if len(fresh["cheat"].Flags) == 0 {
		t.Fatalf("свежий эпизод должен давать флаги")
	}
	old := computeCourseStats(std, students, statuses, base.Add(365*24*time.Hour), nil, nil)
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

	stats := computeCourseStats(std, students, statuses, now, nil, nil)
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
	before := computeCourseStats(std, students, statuses, now, nil, nil)
	if len(before["cheat"].Flags) == 0 {
		t.Fatal("прекондиция: у читера должны быть флаги")
	}
	key := before["cheat"].Flags[0].Key

	// «Перетестирование»: у первой задачи эпизода появилась более ранняя
	// решающая посылка — время первого решения сдвинулось.
	statuses["cheat"].timed[norms[0]] = append(statuses["cheat"].timed[norms[0]],
		source.TimedSubmission{At: base.Add(-30 * time.Minute), Solved: true})
	after := computeCourseStats(std, students, statuses, now, nil, nil)
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

// Цена задачи должна слышать порог понимания, а не только возню с отладкой.
// Две задачи, одинаковые по числу посылок: первую берут все, вторую — половина.
// Прежняя модель (медиана времени) ставила их вровень.
func TestTaskPriceHearsThreshold(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	tasks := []courseTask{{norm: "easy"}, {norm: "hard"}}

	statuses := map[string]*accountStatuses{}
	for i := 0; i < 20; i++ {
		st := newAccountStatuses()
		// «easy» берут все, «hard» — только каждый второй; обеим по 2 посылки.
		put := func(norm string, solved bool, at time.Time) {
			st.attempted[norm] = struct{}{}
			st.timed[norm] = []source.TimedSubmission{{At: at}, {At: at.Add(10 * time.Minute), Solved: solved}}
			if solved {
				st.solved[norm] = struct{}{}
			}
		}
		put("easy", true, base)
		put("hard", i%2 == 0, base.Add(time.Hour))
		statuses[fmt.Sprintf("s%d", i)] = st
	}
	m := fitCourseModel(tasks, statuses, nil)
	if !(m.price["hard"] > m.price["easy"]) {
		t.Fatalf("задача, которую берёт половина, должна стоить дороже: easy=%.2f hard=%.2f",
			m.price["easy"], m.price["hard"])
	}
	// Единица — «обычная задача курса»: медианная задача стоит около 1.
	if med := median([]float64{m.price["easy"], m.price["hard"]}); med < 0.9 || med > 1.1 {
		t.Fatalf("медианная задача должна стоить ≈1, получили %.2f", med)
	}
}

// Темп — про то, сколько человек прошёл, а не про то, как аккуратно он сдаёт.
// Прежняя «скорость» ставила прошедшего весь курс ниже того, кто взял горстку
// задач с первой попытки, потому что мерила промежутки между посылками.
func TestTempoRewardsProgressNotTidySubmissions(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(20 * 7 * 24 * time.Hour)

	tasks := make([]domain.GeneratedTask, 20)
	norms := make([]string, 20)
	for i := range tasks {
		norms[i] = fmt.Sprintf("t%d", i)
		tasks[i] = domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norms[i]}
	}
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}}}

	statuses := map[string]*accountStatuses{}
	students := make([]domain.Student, 0)
	// Фон: по 10 задач за 5 недель, по две посылки на задачу.
	add := func(id string, count, weeks, subsPer int) {
		st := newAccountStatuses()
		for j := 0; j < count; j++ {
			wk := j * weeks / count
			at := base.Add(time.Duration(wk) * 7 * 24 * time.Hour).Add(time.Duration(j) * time.Hour)
			subs := make([]source.TimedSubmission, 0, subsPer)
			for k := 0; k < subsPer-1; k++ {
				subs = append(subs, source.TimedSubmission{At: at.Add(time.Duration(k*5) * time.Minute)})
			}
			subs = append(subs, source.TimedSubmission{At: at.Add(time.Duration(subsPer*5) * time.Minute), Solved: true})
			st.timed[norms[j]] = subs
			st.solved[norms[j]] = struct{}{}
			st.attempted[norms[j]] = struct{}{}
		}
		statuses[id] = st
		students = append(students, domain.Student{ID: id})
	}
	for i := 0; i < 6; i++ {
		add(fmt.Sprintf("bg%d", i), 10, 5, 3)
	}
	// Прошёл весь курс за 10 недель, но отлаживает помногу.
	add("marathon", 20, 10, 6)
	// Взял 6 задач за 6 недель, зато каждую с первой попытки.
	add("tidy", 6, 6, 1)

	stats := computeCourseStats(std, students, statuses, now, nil, nil)
	marathon, tidy := stats["marathon"].Tempo, stats["tidy"].Tempo
	if marathon <= 0 || tidy <= 0 {
		t.Fatalf("темп должен считаться у обоих: marathon=%v tidy=%v", marathon, tidy)
	}
	if !(marathon > tidy) {
		t.Fatalf("прошедший весь курс должен иметь темп выше: marathon=%v tidy=%v", marathon, tidy)
	}
	if stats["marathon"].SolvedCount != 20 {
		t.Fatalf("marathon должен пройти весь курс, решено %d", stats["marathon"].SolvedCount)
	}
}

// Флаг меряет невероятность для КОНКРЕТНОГО ученика: одна и та же серия у
// сильного — норма, у слабого — сигнал.
func TestFlagsWeighStudentsOwnRecord(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(30 * 24 * time.Hour)

	tasks := make([]domain.GeneratedTask, 24)
	norms := make([]string, 24)
	for i := range tasks {
		norms[i] = fmt.Sprintf("t%d", i)
		tasks[i] = domain.GeneratedTask{Label: fmt.Sprintf("%c", 'A'+i), NormalizedURL: norms[i]}
	}
	std := domain.GeneratedGroupStandings{GroupSlug: "g", GroupTitle: "Г",
		Contests: []domain.GeneratedContestStandings{{Title: "K", Tasks: tasks}}}

	statuses := map[string]*accountStatuses{}
	students := make([]domain.Student, 0)
	// solve: решает первые count задач; firstTryUpto — сколько из них с первой.
	solve := func(id string, count, firstTryUpto int) {
		st := newAccountStatuses()
		for j := 0; j < count; j++ {
			at := base.Add(time.Duration(j) * 8 * time.Hour)
			if j < firstTryUpto {
				st.timed[norms[j]] = []source.TimedSubmission{{At: at, Solved: true}}
			} else {
				st.timed[norms[j]] = []source.TimedSubmission{{At: at}, {At: at.Add(20 * time.Minute), Solved: true}}
			}
			st.solved[norms[j]] = struct{}{}
			st.attempted[norms[j]] = struct{}{}
		}
		statuses[id] = st
		students = append(students, domain.Student{ID: id})
	}
	// Когорта: 12 человек, эти задачи почти никто не берёт с первой попытки.
	for i := 0; i < 12; i++ {
		solve(fmt.Sprintf("s%d", i), 20, 0)
	}
	// Сильный: стабильно берёт с первой попытки — для него серия ожидаема.
	solve("strong", 20, 20)
	// Слабый: 16 задач мучил, а потом 5 подряд взял с первой.
	weak := newAccountStatuses()
	for j := 0; j < 16; j++ {
		at := base.Add(time.Duration(j) * 8 * time.Hour)
		weak.timed[norms[j]] = []source.TimedSubmission{{At: at}, {At: at.Add(30 * time.Minute), Solved: true}}
		weak.solved[norms[j]] = struct{}{}
		weak.attempted[norms[j]] = struct{}{}
	}
	for j := 16; j < 21; j++ {
		at := base.Add(20 * 24 * time.Hour).Add(time.Duration(j-16) * 3 * time.Minute)
		weak.timed[norms[j]] = []source.TimedSubmission{{At: at, Solved: true}}
		weak.solved[norms[j]] = struct{}{}
		weak.attempted[norms[j]] = struct{}{}
	}
	statuses["weak"] = weak
	students = append(students, domain.Student{ID: "weak"})

	stats := computeCourseStats(std, students, statuses, now, nil, nil)
	if n := len(stats["weak"].Flags); n == 0 {
		t.Fatalf("серия, нетипичная для этого ученика, должна дать флаг")
	}
	if n := len(stats["strong"].Flags); n != 0 {
		t.Fatalf("тому, кто всегда берёт с первой попытки, флаг не нужен: %+v", stats["strong"].Flags)
	}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("s%d", i)
		if n := len(stats[id].Flags); n != 0 {
			t.Fatalf("у обычного ученика %s флагов быть не должно: %+v", id, stats[id].Flags)
		}
	}
}

// Оценка по условию должна работать там, где данных ещё нет: у новой задачи
// цена берётся из оценки целиком, а не приравнивается к «обычной задаче».
func TestTaskRatingPricesUnseenTask(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	tasks := []courseTask{{norm: "seen"}, {norm: "fresh"}}

	statuses := map[string]*accountStatuses{}
	for i := 0; i < 20; i++ {
		st := newAccountStatuses()
		st.attempted["seen"] = struct{}{}
		st.solved["seen"] = struct{}{}
		st.timed["seen"] = []source.TimedSubmission{{At: base}, {At: base.Add(5 * time.Minute), Solved: true}}
		statuses[fmt.Sprintf("s%d", i)] = st
	}
	// «fresh» никто не трогал, но она оценена как заметно более трудная.
	ratings := domain.TaskRatings{
		"seen":  {SolveRate: 0.9, Attempts: 2},
		"fresh": {SolveRate: 0.2, Attempts: 6},
	}
	withRating := fitCourseModel(tasks, statuses, ratings)
	without := fitCourseModel(tasks, statuses, nil)

	if !(withRating.price["fresh"] > withRating.price["seen"]) {
		t.Fatalf("оценённая как трудная задача должна стоить дороже: fresh=%.2f seen=%.2f",
			withRating.price["fresh"], withRating.price["seen"])
	}
	// Без оценки про «fresh» ничего не известно и она не дороже «seen».
	if without.price["fresh"] > without.price["seen"] {
		t.Fatalf("без оценки незнакомая задача не должна быть дороже: fresh=%.2f seen=%.2f",
			without.price["fresh"], without.price["seen"])
	}
}

// Данные должны вытеснять оценку: у задачи с сотнями решивших ошибка оценщика
// почти не видна, иначе она осталась бы в цене навсегда.
//
// Курс здесь из восьми разных по трудности задач: на двух задачах робастная
// нормировка вырождается (медиана и MAD считаются по двум числам), и цены
// перестают зависеть от величины расхождения.
func TestTaskRatingYieldsToData(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	norms := []string{"a", "t1", "t2", "t3", "t4", "t5", "t6", "t7"}
	tasks := make([]courseTask, 0, len(norms))
	for _, n := range norms {
		tasks = append(tasks, courseTask{norm: n})
	}
	// Ученик берёт задачу t{k} с вероятностью тем меньшей, чем больше k, и
	// тратит на неё тем больше посылок; «a» по данным — из лёгких.
	build := func(n int) map[string]*accountStatuses {
		out := map[string]*accountStatuses{}
		for i := 0; i < n; i++ {
			st := newAccountStatuses()
			for k, norm := range norms {
				st.attempted[norm] = struct{}{}
				solved := i%(k+2) != 0
				subs := []source.TimedSubmission{{At: base}}
				for j := 0; j < k/2; j++ {
					subs = append(subs, source.TimedSubmission{At: base.Add(time.Duration(j+1) * time.Minute)})
				}
				if solved {
					st.solved[norm] = struct{}{}
					subs = append(subs, source.TimedSubmission{At: base.Add(30 * time.Minute), Solved: true})
				}
				st.timed[norm] = subs
			}
			out[fmt.Sprintf("s%d", i)] = st
		}
		return out
	}
	// Оценка грубо врёт про «a»: якобы её почти никто не берёт и уходит 9 посылок.
	ratings := domain.TaskRatings{"a": {SolveRate: 0.05, Attempts: 9}}

	skew := func(n int) float64 {
		statuses := build(n)
		with := fitCourseModel(tasks, statuses, ratings)
		without := fitCourseModel(tasks, statuses, nil)
		return math.Abs(with.price["a"] - without.price["a"])
	}
	few, many := skew(6), skew(240)
	if !(many < few/2) {
		t.Fatalf("с ростом данных влияние ошибочной оценки должно заметно падать: мало=%.3f много=%.3f", few, many)
	}
}

// Очередь проверки: сверху задачи, где оценка сильнее всего спорит с фактом.
func TestTaskReviewQueueRanksDisagreement(t *testing.T) {
	base := time.Date(2026, 7, 1, 18, 0, 0, 0, time.UTC)
	now := base.Add(24 * time.Hour)
	tasksByNorm := map[string]courseTask{
		"agree":  {norm: "agree", label: "K · A"},
		"argue":  {norm: "argue", label: "K · B"},
		"norate": {norm: "norate", label: "K · C"},
	}
	statuses := map[string]*accountStatuses{}
	for i := 0; i < 20; i++ {
		st := newAccountStatuses()
		for _, norm := range []string{"agree", "argue", "norate"} {
			st.attempted[norm] = struct{}{}
			// Обе задачи берут почти все.
			if i < 18 {
				st.solved[norm] = struct{}{}
				st.timed[norm] = []source.TimedSubmission{{At: base, Solved: true}}
			} else {
				st.timed[norm] = []source.TimedSubmission{{At: base}}
			}
		}
		statuses[fmt.Sprintf("s%d", i)] = st
	}
	ratings := domain.TaskRatings{
		"agree": {SolveRate: 0.9, Attempts: 1}, // согласна с фактом
		"argue": {SolveRate: 0.1, Attempts: 1}, // спорит: «почти никто не возьмёт»
	}
	review := buildTaskReview(tasksByNorm, statuses, ratings, now)
	if review == nil || len(review.Rows) != 2 {
		t.Fatalf("в очередь должны попасть только оценённые задачи: %+v", review)
	}
	if review.Rows[0].NormalizedURL != "argue" {
		t.Fatalf("сверху должна быть спорная задача, а не %q", review.Rows[0].NormalizedURL)
	}
	if !review.Rows[0].Harder {
		t.Error("оценщик считает задачу труднее факта — это должно быть помечено")
	}
	if review.Rows[0].Gap <= review.Rows[1].Gap {
		t.Errorf("расхождение спорной должно быть больше: %.2f против %.2f", review.Rows[0].Gap, review.Rows[1].Gap)
	}
	if review.Rated != 2 || review.Total != 3 {
		t.Errorf("оценено/всего: %d/%d, ожидалось 2/3", review.Rated, review.Total)
	}
}
