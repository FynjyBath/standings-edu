package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Сквозная проверка раздела «Оценки задач»: открыть очередь, сохранить правку,
// снова открыть. Правка должна быть видна СРАЗУ, не дожидаясь генерации:
// иначе преподаватель видит старый балл и правит его второй раз.
func TestTaskRatingsPageShowsSavedEditImmediately(t *testing.T) {
	h, dataDir, genDir := ratingsHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "task_ratings.json"),
		`{"https://x/1":{"idea_rate":0.2,"impl_attempts":4,"model":"test"}}`)
	writeTestFile(t, filepath.Join(genDir, "task_review.json"), `{
		"generated_at":"2026-09-17T10:00:00Z","rated":1,"total":1,
		"rows":[{"normalized_url":"https://x/1","url":"https://x/1","label":"Контест · B",
		         "name":"Улитка","tried":40,"solved":30,"fact_first_try":0.75,
		         "fact_attempts":1.5,"rated_idea_rate":0.2,"rated_impl_attempts":4,
		         "idea_score":10,"impl_score":10,
		         "gap":2.7,"impact":40,"harder":true}]}`)

	// Смотрим именно поле идейности: у второй оси балл свой и не меняется.
	ideaValue := func(body string) string {
		i := strings.Index(body, "data-idea")
		if i < 0 {
			t.Fatalf("на странице нет поля идейности:\n%s", body)
		}
		rest := body[i:]
		j := strings.Index(rest, `value="`)
		if j < 0 {
			t.Fatalf("у поля идейности нет значения:\n%s", rest[:120])
		}
		rest = rest[j+len(`value="`):]
		return rest[:strings.Index(rest, `"`)]
	}

	before := ratingsPage(t, h)
	if got := ideaValue(before); got != "10" {
		t.Fatalf("до правки ожидался балл идейности 10, получили %q", got)
	}
	if strings.Contains(before, "проверено") {
		t.Error("до правки задача не должна быть помечена проверенной")
	}

	body := strings.NewReader(`{"url":"https://x/1","idea_score":3,"by":"Антон"}`)
	rec := httptest.NewRecorder()
	h.AdminTaskRatingValidate(rec, httptest.NewRequest(http.MethodPost, "/x", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("сохранение: code=%d body=%s", rec.Code, rec.Body.String())
	}
	raw, _ := os.ReadFile(filepath.Join(dataDir, "task_ratings.json"))
	if !strings.Contains(string(raw), "validated_by") {
		t.Fatalf("правка не легла в файл: %s", raw)
	}

	after := ratingsPage(t, h)
	if got := ideaValue(after); got != "3" {
		t.Errorf("после правки ожидался балл идейности 3, получили %q", got)
	}
	if !strings.Contains(after, "проверено") {
		t.Error("после правки задача должна быть помечена проверенной")
	}
}

// «Оценка верна» помечает оценку проверенной, НЕ трогая числа. Это отдельный
// смысл: расходится с фактом не оценка, а сама задача (разошлись решения,
// слабые тесты, непонятная формулировка).
func TestTaskRatingKeepMarksValidatedWithoutChangingNumbers(t *testing.T) {
	h, dataDir, _ := ratingsHandlers(t)
	path := filepath.Join(dataDir, "task_ratings.json")
	writeTestFile(t, path, `{"https://x/1":{"idea_rate":0.2,"impl_attempts":4,"model":"test"}}`)

	body := strings.NewReader(`{"url":"https://x/1","by":"Антон","note":"вопрос к задаче"}`)
	rec := httptest.NewRecorder()
	h.AdminTaskRatingValidate(rec, httptest.NewRequest(http.MethodPost, "/x", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{`"idea_rate":0.2`, `"impl_attempts":4`, `"validated_by":"Антон"`} {
		if !strings.Contains(strings.ReplaceAll(got, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("в файле нет %q: %s", want, got)
		}
	}
}

// Нечитаемый файл оценок должен быть виден НА СТРАНИЦЕ, а не только в логе:
// чинит его тот, кто сейчас в админке.
func TestTaskRatingsPageShowsBrokenFile(t *testing.T) {
	h, dataDir, genDir := ratingsHandlers(t)
	writeTestFile(t, filepath.Join(dataDir, "task_ratings.json"),
		`{"https://x/1":{"solve_rate":0.97,"attempts":2.1}}`) // снятый формат
	writeTestFile(t, filepath.Join(genDir, "task_review.json"), `{
		"generated_at":"2026-09-17T10:00:00Z","rated":1,"total":1,
		"rows":[{"normalized_url":"https://x/1","label":"К · B","tried":40,"solved":30,
		         "fact_first_try":0.75,"rated_idea_rate":0.2,"rated_impl_attempts":4,
		         "idea_score":10,"impl_score":10,"gap":2.7,"impact":40}]}`)

	body := ratingsPage(t, h)
	for _, want := range []string{"Файл оценок не читается", "idea_rate", "переоценить"} {
		if !strings.Contains(body, want) {
			t.Errorf("на странице нет %q", want)
		}
	}
}
