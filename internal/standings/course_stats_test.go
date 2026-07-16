package standings

import (
	"fmt"
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

	stats := computeCourseStats(std, students, statuses, now)
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
	stats := computeCourseStats(std, students, statuses, now)
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
