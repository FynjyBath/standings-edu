package httpapi

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
	"standings-edu/internal/web"
)

// ratingsHandlers — Handlers с generated-каталогом, куда можно положить очередь.
func ratingsHandlers(t *testing.T) (*Handlers, string, string) {
	t.Helper()
	dataDir, genDir := t.TempDir(), t.TempDir()
	h := NewHandlers(
		storage.NewGeneratedLoader(genDir), nil,
		web.NewTemplateRenderer(filepath.Join("..", "..", "web", "templates")),
		log.New(io.Discard, "", 0),
	)
	if err := h.ConfigureAdmin(AdminConfig{
		Login: "admin", Password: "pw", ProjectRoot: t.TempDir(), DataDir: dataDir,
	}); err != nil {
		t.Fatalf("ConfigureAdmin: %v", err)
	}
	h.ConfigureSourceDir(dataDir)
	return h, dataDir, genDir
}

func ratingsPage(t *testing.T, h *Handlers) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.AdminTaskRatingsPage(rec, httptest.NewRequest(http.MethodGet, "/standings/admin/task-ratings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("страница оценок: code=%d", rec.Code)
	}
	return rec.Body.String()
}

// Без заведённых оценок страница объясняет, как их завести, а не показывает
// пустую таблицу.
func TestTaskRatingsPageWithoutRatings(t *testing.T) {
	h, _, _ := ratingsHandlers(t)
	body := ratingsPage(t, h)
	// Именно новые ключи: подстрокой «attempts» проверять нельзя — она есть и
	// в предупреждении про снятый формат, и тест проходил бы при неверном
	// образце JSON.
	for _, want := range []string{"Оценок пока нет", "task_ratings.json", "idea_rate", "impl_attempts"} {
		if !strings.Contains(body, want) {
			t.Errorf("на странице нет %q", want)
		}
	}
	// Образец не должен показывать снятые ключи как рабочие: про них на
	// странице сказано отдельно, что файл с ними читать откажутся.
	if strings.Contains(body, `"solve_rate"`) {
		t.Error("в образце JSON остались ключи снятого формата")
	}
}

// Очередь показывает сырые наблюдаемые рядом с предсказанием — преподаватель
// должен видеть факт, а не итоговую цену, на которую сам же и повлияет.
func TestTaskRatingsPageShowsQueue(t *testing.T) {
	h, _, genDir := ratingsHandlers(t)
	writeTestFile(t, filepath.Join(genDir, "task_review.json"), `{
		"generated_at":"2026-09-17T10:00:00Z","rated":2,"total":9,
		"rows":[{"normalized_url":"https://x/1","url":"https://x/1","label":"Контест · B",
		         "name":"Улитка","tried":40,"solved":30,"fact_first_try":0.75,
		         "fact_attempts":1.5,"rated_idea_rate":0.2,"rated_impl_attempts":4,
		         "idea_score":7.5,"impl_score":2.5,
		         "gap":2.7,"impact":40,"harder":true}]}`)
	body := ratingsPage(t, h)
	for _, want := range []string{
		"Контест · B", "Улитка",
		"75%", // факт: доля взявших с первой посылки
		"20%", // во что переводится балл идейности
		"Оценено 2 задач из 9",
		`value="7.5"`, `value="2.5"`, // баллы обеих осей в форме правки
		"Сохранить", "Оценка верна",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на странице нет %q", want)
		}
	}
}

// Подтверждение пишет оценку на диск и помечает её проверенной: от этого
// зависит вес априора в модели, так что отметка не косметическая.
func TestTaskRatingValidateWrites(t *testing.T) {
	h, dataDir, _ := ratingsHandlers(t)
	path := filepath.Join(dataDir, "task_ratings.json")
	writeTestFile(t, path, `{"https://x/1":{"idea_rate":0.2,"impl_attempts":4,"model":"test"}}`)

	// Правка приходит в баллах 1..10 — так её вводит человек.
	body := strings.NewReader(`{"url":"https://x/1","idea_score":3,"impl_score":8,"by":"Антон","note":"поправил"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/task-rating/validate", body)
	rec := httptest.NewRecorder()
	h.AdminTaskRatingValidate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved domain.TaskRatings
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	got, ok := saved[domain.NormalizeTaskURL("https://x/1")]
	if !ok {
		t.Fatalf("оценка не сохранилась: %s", raw)
	}
	// Баллы переводятся в наблюдаемые величины; проверяем, что обратный
	// перевод даёт те же баллы — иначе правка «уезжает» при каждом открытии.
	if gotIdea := got.IdeaScore(); math.Abs(gotIdea-3) > 0.1 {
		t.Errorf("идейность не записалась: балл %v, оценка %+v", gotIdea, got)
	}
	if gotImpl := got.ImplScore(); math.Abs(gotImpl-8) > 0.1 {
		t.Errorf("реализация не записалась: балл %v, оценка %+v", gotImpl, got)
	}
	if !got.Validated() || got.ValidatedBy != "Антон" {
		t.Errorf("оценка должна стать подтверждённой: %+v", got)
	}
	if got.Model != "test" {
		t.Errorf("прежние поля должны сохраниться: %+v", got)
	}
}

// Негодные числа не должны попадать в файл: балл вне 1..10 после перевода дал
// бы долю вне (0,1) и сломал логиты.
func TestTaskRatingValidateRejectsNonsense(t *testing.T) {
	h, dataDir, _ := ratingsHandlers(t)
	path := filepath.Join(dataDir, "task_ratings.json")
	writeTestFile(t, path, `{"https://x/1":{"idea_rate":0.2,"impl_attempts":4}}`)

	for _, bad := range []string{
		`{"url":"https://x/1","idea_score":14}`,
		`{"url":"https://x/1","impl_score":0}`,
		`{"url":"","idea_score":5}`,
	} {
		rec := httptest.NewRecorder()
		h.AdminTaskRatingValidate(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(bad)))
		if rec.Code == http.StatusOK {
			t.Errorf("должно быть отклонено: %s", bad)
		}
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "validated_by") {
		t.Error("отклонённый запрос не должен править файл")
	}
}

// Страница и эндпоинт закрыты админской авторизацией: оценки влияют на цены
// задач у всех групп.
func TestTaskRatingsRoutesNeedAdmin(t *testing.T) {
	h, _, _ := ratingsHandlers(t)
	router := NewRouter(h, t.TempDir())
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/standings/admin/task-ratings"},
		{http.MethodPost, "/api/admin/task-rating/validate"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s без авторизации: code=%d, ожидался 401", c.method, c.path, rec.Code)
		}
	}
}
