package standings

// Временный сетевой тест для проверки гипотезы о завышенной скорости у
// учеников, сдающих с первой попытки (время на обдумывание до первой посылки
// не учитывается). Запуск: VERIFY_SPEED=1 go test -run TestVerifySpeedKomarov
// НЕ КОММИТИТЬ.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"testing"
	"time"

	"standings-edu/internal/source"
)

func TestVerifySpeedKomarov(t *testing.T) {
	if os.Getenv("VERIFY_SPEED") == "" {
		t.Skip("set VERIFY_SPEED=1")
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)

	cfClient, err := source.NewCodeforcesAPIClientFromFileWithState("../../data/credentials/codeforces_credentials.json", "")
	if err != nil {
		t.Fatalf("cf: %v", err)
	}
	infClient, err := source.NewInformaticsAPIClientFromFileWithState("../../data/credentials/informatics_credentials.json", "")
	if err != nil {
		t.Fatalf("inf: %v", err)
	}
	registry := source.NewRegistry()
	registry.RegisterSite("codeforces", cfClient)
	registry.RegisterSite("informatics", infClient)
	b := NewBuilder(registry, logger, 4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	students := map[string][][2]string{
		"komarov-mi": {{"informatics", "723972"}, {"codeforces", "MasterMixail"}},
		"kurzin-ia":  {{"informatics", "463354"}, {"codeforces", "IgorKurzin"}},
	}

	// Задачи курса smip_2026_p1 (normalized URL) из прод-API.
	var courseNorms []string
	{
		body, err := os.ReadFile("/tmp/claude-1000/-home-fynjybath-standings-edu/b538ef21-324c-4a68-9a6b-85e7bff8d853/scratchpad/p1_course_norms.json")
		if err != nil {
			t.Fatalf("course norms: %v", err)
		}
		if err := json.Unmarshal(body, &courseNorms); err != nil {
			t.Fatal(err)
		}
	}
	course := make(map[string]bool, len(courseNorms))
	for _, n := range courseNorms {
		course[n] = true
	}

	for sid, accounts := range students {
		combined := newAccountStatuses()
		for _, acc := range accounts {
			st, err := b.fetchAccountStatuses(ctx, acc[0], acc[1])
			if err != nil {
				t.Fatalf("%s %s/%s: %v", sid, acc[0], acc[1], err)
			}
			mergeStatuses(combined, st)
		}
		analyze(sid, combined)
		analyzeCourse(sid, combined, course)
	}
}

// analyzeCourse — тот же разбор, но только по задачам курса: активное время,
// попытки до AC, задачи, решённые «одинокой» посылкой (вся сессия = 1 событие).
func analyzeCourse(sid string, st *accountStatuses, course map[string]bool) {
	tt := buildStudentTaskTimes(st)

	activeCourse := 0.0
	for norm, m := range tt.taskMin {
		if course[norm] {
			activeCourse += m
		}
	}

	// Сессии заново (для проверки одиночности решающей посылки).
	type ev struct {
		at     time.Time
		norm   string
		solved bool
	}
	events := make([]ev, 0)
	for norm, subs := range st.timed {
		for _, s := range subs {
			events = append(events, ev{s.At, norm, s.Solved})
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	sessionID := make([]int, len(events))
	sizes := map[int]int{}
	cur := -1
	for i := range events {
		if i == 0 || events[i].at.Sub(events[i-1].at).Minutes() > courseSessionGapMin {
			cur++
		}
		sessionID[i] = cur
		sizes[cur]++
	}

	firstTry, soloSession, total := 0, 0, 0
	timeSamples := []float64{}
	for norm := range st.solved {
		if !course[norm] {
			continue
		}
		subs := st.timed[norm]
		if len(subs) == 0 {
			continue
		}
		total++
		if m := tt.taskMin[norm]; m > 0 {
			timeSamples = append(timeSamples, m)
		}
		sort.Slice(subs, func(i, j int) bool { return subs[i].At.Before(subs[j].At) })
		if subs[0].Solved {
			firstTry++
			// одиночная ли сессия у решающей посылки
			for i := range events {
				if events[i].norm == norm && events[i].at.Equal(subs[0].At) {
					if sizes[sessionID[i]] == 1 {
						soloSession++
					}
					break
				}
			}
		}
	}
	sort.Float64s(timeSamples)
	med := 0.0
	if len(timeSamples) > 0 {
		med = timeSamples[len(timeSamples)/2]
	}
	fmt.Printf("--- %s ПО КУРСУ p1 ---\n", sid)
	fmt.Printf("активное время на курсе: %.0f мин (%.1f ч)\n", activeCourse, activeCourse/60)
	fmt.Printf("решено задач курса с посылками: %d; с первой попытки: %d; из них решающая посылка была ЕДИНСТВЕННОЙ в сессии: %d\n", total, firstTry, soloSession)
	fmt.Printf("медиана активного времени на решённую задачу курса: %.1f мин\n", med)

	// Дамп по-задачных времён для симуляции floor в python.
	type dumpRow struct {
		Norm   string  `json:"norm"`
		Min    float64 `json:"min"`
		Solved bool    `json:"solved"`
	}
	rows := []dumpRow{}
	for norm, m := range tt.taskMin {
		if !course[norm] || m <= 0 {
			continue
		}
		_, solved := st.solved[norm]
		rows = append(rows, dumpRow{norm, m, solved})
	}
	blob, _ := json.Marshal(rows)
	_ = os.WriteFile("/tmp/claude-1000/-home-fynjybath-standings-edu/b538ef21-324c-4a68-9a6b-85e7bff8d853/scratchpad/times_"+sid+".json", blob, 0o644)
}

func analyze(sid string, st *accountStatuses) {
	tt := buildStudentTaskTimes(st)

	// δ0 так же, как в buildStudentTaskTimes.
	type ev struct {
		at     time.Time
		norm   string
		solved bool
	}
	events := make([]ev, 0)
	for norm, subs := range st.timed {
		for _, s := range subs {
			events = append(events, ev{s.At, norm, s.Solved})
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	gaps := []float64{}
	for i := 1; i < len(events); i++ {
		g := events[i].at.Sub(events[i-1].at).Minutes()
		if g > 0 && g <= courseSessionGapMin {
			gaps = append(gaps, g)
		}
	}
	delta0 := courseDelta0MaxMin
	if len(gaps) > 0 {
		if m := median(gaps); m < delta0 {
			delta0 = m
		}
	}

	// Разложение активного времени: входные бонусы (первое событие сессии)
	// vs внутрисессионные паузы.
	entryMin, intraMin := 0.0, 0.0
	singletonSessions, totalSessions := 0, 0
	cnt := 0
	for i := range events {
		newSession := i == 0 || events[i].at.Sub(events[i-1].at).Minutes() > courseSessionGapMin
		if newSession {
			totalSessions++
			if cnt == 1 {
				singletonSessions++
			}
			cnt = 1
			entryMin += delta0
		} else {
			intraMin += events[i].at.Sub(events[i-1].at).Minutes()
			cnt++
		}
	}
	if cnt == 1 {
		singletonSessions++
	}

	// По решённым задачам: сколько timed-посылок ушло на решение (до AC вкл.)?
	firstTry, total := 0, 0
	distr := map[int]int{}
	for norm := range st.solved {
		subs := st.timed[norm]
		if len(subs) == 0 {
			continue
		}
		sort.Slice(subs, func(i, j int) bool { return subs[i].At.Before(subs[j].At) })
		n := 0
		for _, s := range subs {
			n++
			if s.Solved {
				break
			}
		}
		total++
		if n == 1 {
			firstTry++
		}
		if n > 5 {
			n = 6
		}
		distr[n]++
	}

	totalActive := 0.0
	for _, m := range tt.taskMin {
		totalActive += m
	}

	fmt.Printf("\n===== %s =====\n", sid)
	fmt.Printf("посылок с временем: %d; сессий: %d (из них одиночных: %d)\n", len(events), totalSessions, singletonSessions)
	fmt.Printf("δ0 = %.1f мин (медиана внутрисессионных пауз, гэпов=%d)\n", delta0, len(gaps))
	fmt.Printf("активное время всего: %.0f мин = входные бонусы %.0f мин (%.0f%%) + внутрисессионное %.0f мин\n",
		totalActive, entryMin, entryMin/totalActive*100, intraMin)
	fmt.Printf("решённых задач с посылками: %d, из них С ПЕРВОЙ попытки: %d (%.0f%%)\n", total, firstTry, float64(firstTry)/float64(total)*100)
	keys := []int{1, 2, 3, 4, 5, 6}
	for _, k := range keys {
		if distr[k] > 0 {
			label := fmt.Sprintf("%d", k)
			if k == 6 {
				label = ">5"
			}
			fmt.Printf("  попыток до AC=%s: %d задач\n", label, distr[k])
		}
	}
}
