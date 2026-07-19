package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/web"
)

// Подпись «Последнее обновление» под таблицей контеста показывается только у
// контестов с update=false (NotUpdated) или если вся группа в архиве.
func TestGroupStandingsContestUpdatedCaption(t *testing.T) {
	renderer := web.NewTemplateRenderer("../../web/templates")
	gen := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	page := func(notUpdated, groupArchived bool) GroupPageData {
		return GroupPageData{
			PageTitle:     "t",
			Footer:        FooterInfo{},
			GroupArchived: groupArchived,
			Standings: domain.GeneratedGroupStandings{
				GroupSlug:  "alpha",
				GroupTitle: "Альфа",
				Contests: []domain.GeneratedContestStandings{{
					ID:          "c1",
					Title:       "Контест",
					ScoreSystem: domain.ScoreSystemEdu,
					GeneratedAt: &gen,
					NotUpdated:  notUpdated,
					Rows: []domain.GeneratedRow{
						{StudentID: "stud1", PublicName: "Иванов И.", Place: "1", Statuses: []string{}},
					},
				}},
			},
		}
	}

	render := func(d GroupPageData) string {
		rec := httptest.NewRecorder()
		if err := renderer.Render(rec, 200, "group_standings.html", d); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}

	// Именно подпись под таблицей контеста (в футере тот же текст есть всегда).
	const caption = "contest-generated-at"

	if got := render(page(false, false)); strings.Contains(got, caption) {
		t.Fatal("у активного контеста активной группы подписи быть не должно")
	}
	if got := render(page(true, false)); !strings.Contains(got, caption) {
		t.Fatal("у контеста с update=false должна быть подпись")
	}
	if got := render(page(false, true)); !strings.Contains(got, caption) {
		t.Fatal("у контеста архивной группы должна быть подпись")
	}
}

// Имена учеников в таблицах группы кликабельны (ведут на профиль) только при
// валидном токене преподавателя; ученикам (без токена) — обычный текст.
func TestGroupStandingsClickableNames(t *testing.T) {
	renderer := web.NewTemplateRenderer("../../web/templates")

	page := func(tokenValid bool) GroupPageData {
		return GroupPageData{
			PageTitle:  "t",
			Footer:     FooterInfo{},
			Token:      "T0K",
			TokenValid: tokenValid,
			Standings: domain.GeneratedGroupStandings{
				GroupSlug:          "alpha",
				GroupTitle:         "Альфа",
				SolvedSummarySites: []string{"cf"},
				SolvedSummary: []domain.GeneratedGroupSolvedSummaryRow{
					{StudentID: "stud1", PublicName: "Иванов И.", SolvedCountOnPageSites: 1, SolvedCountBySite: []int{1}},
				},
				Contests: []domain.GeneratedContestStandings{{
					ID:          "c1",
					Title:       "Контест",
					ScoreSystem: domain.ScoreSystemEdu,
					Rows: []domain.GeneratedRow{
						{StudentID: "stud1", PublicName: "Иванов И.", Place: "1", SolvedCount: 1, Statuses: []string{}},
					},
				}},
			},
		}
	}

	render := func(d GroupPageData) string {
		rec := httptest.NewRecorder()
		if err := renderer.Render(rec, 200, "group_standings.html", d); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}

	link := `href="/standings/alpha/student?id=stud1&token=T0K"`

	withTok := render(page(true))
	// Ссылка должна быть и в основной таблице, и в доске «решено» — минимум дважды.
	if n := strings.Count(withTok, link); n < 2 {
		t.Fatalf("под токеном имена должны быть ссылками (найдено %d, ждём ≥2): %q", n, link)
	}

	noTok := render(page(false))
	if strings.Contains(noTok, link) {
		t.Fatal("без токена имена не должны вести на профиль")
	}
	if !strings.Contains(noTok, "Иванов И.") {
		t.Fatal("имя должно остаться обычным текстом")
	}
}
