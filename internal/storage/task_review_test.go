package storage

import (
	"testing"
	"time"

	"standings-edu/internal/domain"
)

// Очередь пишет генератор, а читает сервер — проверяем, что они сходятся по
// именам полей: разъехаться они могут молча, файл просто прочитается пустым.
func TestTaskReviewRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := &GeneratedWriter{OutDir: dir}
	want := domain.GeneratedTaskReview{
		GeneratedAt: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
		Rated:       2, Total: 9,
		Rows: []domain.GeneratedTaskReviewRow{{
			NormalizedURL: "https://informatics.msk.ru/mod/statements/view.php?chapterid=2955",
			URL:           "https://informatics.msk.ru/mod/statements/view.php?chapterid=2955",
			Label:         "Контест · R", Name: "Улитка",
			Tried: 40, Solved: 30, FactSolveRate: 0.75, FactAttempts: 1.5,
			RatedSolveRate: 0.2, RatedAttempts: 4,
			Gap: 2.7, Impact: 40, Harder: true,
		}},
	}
	if err := w.WriteTaskReview(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := NewGeneratedLoader(dir).LoadTaskReview()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Rated != want.Rated || got.Total != want.Total {
		t.Errorf("оценено/всего: %d/%d, ожидалось %d/%d", got.Rated, got.Total, want.Rated, want.Total)
	}
	if len(got.Rows) != 1 {
		t.Fatalf("строк %d, ожидалась одна", len(got.Rows))
	}
	g, wRow := got.Rows[0], want.Rows[0]
	if g.Label != wRow.Label || g.Name != wRow.Name || g.NormalizedURL != wRow.NormalizedURL {
		t.Errorf("не совпали поля задачи: %+v", g)
	}
	if g.Tried != wRow.Tried || g.Solved != wRow.Solved || g.FactSolveRate != wRow.FactSolveRate ||
		g.RatedSolveRate != wRow.RatedSolveRate || g.RatedAttempts != wRow.RatedAttempts {
		t.Errorf("не совпали числа: %+v", g)
	}
	if !g.Harder || g.Gap != wRow.Gap || g.Impact != wRow.Impact {
		t.Errorf("не совпало расхождение: %+v", g)
	}
}

// Файла может не быть (оценок не завели, генерация не проходила) — это норма.
func TestTaskReviewMissingFileIsEmpty(t *testing.T) {
	got, err := NewGeneratedLoader(t.TempDir()).LoadTaskReview()
	if err != nil {
		t.Fatalf("отсутствующий файл не должен быть ошибкой: %v", err)
	}
	if len(got.Rows) != 0 || got.Rated != 0 {
		t.Errorf("ожидалась пустая очередь, получили %+v", got)
	}
}
