package standings

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// CPU-стоимость сборки standings на 800 учеников × 41 контест × 8 задач
// (без сети: статусы уже собраны).
func TestPerfProbeBuild(t *testing.T) {
	if os.Getenv("PERF_PROBE") == "" {
		t.Skip("перф-замер: запуск PERF_PROBE=1 go test -run TestPerfProbe")
	}
	nStudents, nContests, nTasks := 800, 41, 8
	students := make(map[string]domain.Student, nStudents)
	ids := make([]string, nStudents)
	for s := 0; s < nStudents; s++ {
		id := fmt.Sprintf("s%d", s)
		ids[s] = id
		students[id] = domain.Student{ID: id, PublicName: fmt.Sprintf("Ученик %04d", s)}
	}
	contests := make(map[string]domain.Contest, nContests)
	refs := make([]domain.GroupContestRef, 0, nContests)
	for c := 0; c < nContests; c++ {
		cid := fmt.Sprintf("c%d", c)
		tasks := make([]string, nTasks)
		for tt := 0; tt < nTasks; tt++ {
			tasks[tt] = fmt.Sprintf("https://example.com/task/%d/%d", c, tt)
		}
		contests[cid] = domain.Contest{ID: cid, Title: cid, ScoreSystem: domain.ScoreSystemEdu,
			ContestType: domain.ContestTypeTasks,
			Subcontests: []domain.Subcontest{{Title: "T", Tasks: tasks}}}
		refs = append(refs, domain.GroupContestRef{ID: cid, Update: true})
	}
	b := NewBuilder(source.NewRegistry(), log.New(io.Discard, "", 0), 8)
	data := &domain.SourceData{Students: students, Contests: contests}
	groups := []domain.GroupDefinition{{Slug: "g", Title: "G", StudentIDs: ids, Contests: refs}}

	t0 := time.Now()
	res, _, err := b.BuildGroupsStandings(context.Background(), data, groups)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PERF build 800x41x8 (no net): %v, contests=%d\n", time.Since(t0), len(res["g"].Contests))
}
