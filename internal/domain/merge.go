package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// AssignContestPlaces проставляет места по уже отсортированным строкам таблицы.
// Равный ранг (одинаковый счёт: TotalScore для ioi, SolvedCount для edu) получает
// один и тот же диапазон мест ("3-5").
func AssignContestPlaces(rows []GeneratedRow, isIOI bool) {
	sameRank := func(a, b GeneratedRow) bool {
		if isIOI {
			return a.TotalScore == b.TotalScore
		}
		return a.SolvedCount == b.SolvedCount
	}

	i := 0
	for i < len(rows) {
		j := i + 1
		for j < len(rows) && sameRank(rows[i], rows[j]) {
			j++
		}
		if j-i == 1 {
			rows[i].Place = fmt.Sprintf("%d", i+1)
		} else {
			place := fmt.Sprintf("%d-%d", i+1, j)
			for k := i; k < j; k++ {
				rows[k].Place = place
			}
		}
		i = j
	}
}

// sortContestRows сортирует строки так же, как при сборке обычной таблицы: ioi —
// по сумме, затем по числу решённых; edu — по числу решённых; при равенстве — по
// имени.
func sortContestRows(rows []GeneratedRow, isIOI bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		if isIOI {
			if rows[i].TotalScore != rows[j].TotalScore {
				return rows[i].TotalScore > rows[j].TotalScore
			}
		}
		if rows[i].SolvedCount != rows[j].SolvedCount {
			return rows[i].SolvedCount > rows[j].SolvedCount
		}
		return strings.ToLower(rows[i].PublicName) < strings.ToLower(rows[j].PublicName)
	})
}

// MergeGroupStandings объединяет таблицы нескольких групп в одну «сводную группу».
// Контесты с одинаковым id (одна и та же ссылка/контест) сливаются в одну таблицу
// с объединением учеников и пересчётом мест; контесты с разными id остаются
// отдельными таблицами. Порядок контестов — по первому появлению среди members.
// Оценки в сводной группе не собираются (у неё нет своей конфигурации оценок).
func MergeGroupStandings(slug, title string, members []GeneratedGroupStandings) GeneratedGroupStandings {
	out := GeneratedGroupStandings{
		GroupSlug:  slug,
		GroupTitle: title,
	}

	order := make([]string, 0)
	contribs := make(map[string][]contestContrib)
	for _, member := range members {
		tag := strings.TrimSpace(member.GroupShortName)
		for _, contest := range member.Contests {
			if _, ok := contribs[contest.ID]; !ok {
				order = append(order, contest.ID)
			}
			contribs[contest.ID] = append(contribs[contest.ID], contestContrib{standings: contest, tag: tag})
		}
	}

	out.Contests = make([]GeneratedContestStandings, 0, len(order))
	for _, id := range order {
		out.Contests = append(out.Contests, mergeContestContribs(contribs[id]))
	}

	out.SolvedSummarySites, out.SolvedSummary = mergeSolvedSummaries(members)
	return out
}

// contestContrib — вклад одного контеста от одной группы-участницы вместе с её
// коротким названием (tag), которым подписываются участники в объединении.
type contestContrib struct {
	standings GeneratedContestStandings
	tag       string
}

// tagName приписывает короткое название группы в скобках: «Имя (короткое)».
func tagName(name, tag string) string {
	if tag == "" {
		return name
	}
	return name + " (" + tag + ")"
}

// mergeContestContribs сливает вклады одного контеста из разных групп. Метаданные
// берутся из первого вклада; строки объединяются по ученику (дубликат — первый
// вклад побеждает). Если хоть один вклад заморожен, итог считается замороженным:
// публичные строки — из frozen-версий, а полные (rows_full, просмотр по токену) —
// из полных (у незамороженных вкладов полными считаются их же обычные строки).
func mergeContestContribs(contribs []contestContrib) GeneratedContestStandings {
	merged := contribs[0].standings
	merged.Rows = nil
	merged.RowsFull = nil

	isIOI := merged.ScoreSystem.IsIOI()
	frozen := false
	var frozenAt *time.Time
	allHidden := true
	var latest *time.Time

	seen := make(map[string]struct{})
	publicRows := make([]GeneratedRow, 0)
	fullRows := make([]GeneratedRow, 0)

	for _, contrib := range contribs {
		contest := contrib.standings
		if !contest.Hidden {
			allHidden = false
		}
		if contest.FrozenAt != nil {
			frozen = true
			if frozenAt == nil {
				frozenAt = contest.FrozenAt
			}
		}
		if contest.GeneratedAt != nil && (latest == nil || contest.GeneratedAt.After(*latest)) {
			latest = contest.GeneratedAt
		}

		fullByStudent := make(map[string]GeneratedRow, len(contest.RowsFull))
		for _, row := range contest.RowsFull {
			fullByStudent[row.StudentID] = row
		}

		for _, row := range contest.Rows {
			if _, dup := seen[row.StudentID]; dup {
				continue
			}
			seen[row.StudentID] = struct{}{}
			// row/full — копии из range/map: подписываем безопасно, не трогая кэш.
			row.PublicName = tagName(row.PublicName, contrib.tag)
			publicRows = append(publicRows, row)
			if full, ok := fullByStudent[row.StudentID]; ok {
				full.PublicName = tagName(full.PublicName, contrib.tag)
				fullRows = append(fullRows, full)
			} else {
				fullRows = append(fullRows, row)
			}
		}
	}

	merged.Hidden = allHidden
	merged.GeneratedAt = latest

	if len(contribs) > 1 {
		sortContestRows(publicRows, isIOI)
		AssignContestPlaces(publicRows, isIOI)
	}
	merged.Rows = publicRows

	if frozen {
		merged.FrozenAt = frozenAt
		if len(contribs) > 1 {
			sortContestRows(fullRows, isIOI)
			AssignContestPlaces(fullRows, isIOI)
		}
		merged.RowsFull = fullRows
	} else {
		merged.FrozenAt = nil
		merged.RowsFull = nil
	}

	return merged
}

// mergeSolvedSummaries объединяет сводку «решено задач» по сайтам: список сайтов —
// объединение, по каждому ученику берётся его строка (дубликат — первый вклад),
// а поконтестные счётчики переносятся на позиции объединённого списка сайтов.
// Всего решено (TotalSolvedCount) — глобально по ученику, поэтому одинаково в
// любой группе. Итог сортируется по убыванию «всего решено».
func mergeSolvedSummaries(members []GeneratedGroupStandings) ([]string, []GeneratedGroupSolvedSummaryRow) {
	siteSet := make(map[string]struct{})
	for _, member := range members {
		for _, site := range member.SolvedSummarySites {
			siteSet[site] = struct{}{}
		}
	}
	if len(siteSet) == 0 {
		return nil, nil
	}
	sites := make([]string, 0, len(siteSet))
	for site := range siteSet {
		sites = append(sites, site)
	}
	sort.Strings(sites)
	siteIndex := make(map[string]int, len(sites))
	for i, site := range sites {
		siteIndex[site] = i
	}

	seen := make(map[string]struct{})
	rows := make([]GeneratedGroupSolvedSummaryRow, 0)
	for _, member := range members {
		tag := strings.TrimSpace(member.GroupShortName)
		for _, row := range member.SolvedSummary {
			if _, dup := seen[row.StudentID]; dup {
				continue
			}
			seen[row.StudentID] = struct{}{}

			merged := GeneratedGroupSolvedSummaryRow{
				StudentID:         row.StudentID,
				PublicName:        tagName(row.PublicName, tag),
				TotalSolvedCount:  row.TotalSolvedCount,
				SolvedCountBySite: make([]int, len(sites)),
			}
			onPage := 0
			for i, site := range member.SolvedSummarySites {
				if i >= len(row.SolvedCountBySite) {
					break
				}
				count := row.SolvedCountBySite[i]
				if idx, ok := siteIndex[site]; ok {
					merged.SolvedCountBySite[idx] = count
					onPage += count
				}
			}
			merged.SolvedCountOnPageSites = onPage
			rows = append(rows, merged)
		}
	}

	SortSolvedSummary(rows)
	return sites, rows
}

// SortSolvedSummary упорядочивает доску почёта по видимой колонке «Сумма»
// (SolvedCountOnPageSites) по убыванию; при равенстве — по глобально решённым и
// затем по имени. Так порядок совпадает с числами в таблице.
func SortSolvedSummary(rows []GeneratedGroupSolvedSummaryRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SolvedCountOnPageSites != rows[j].SolvedCountOnPageSites {
			return rows[i].SolvedCountOnPageSites > rows[j].SolvedCountOnPageSites
		}
		if rows[i].TotalSolvedCount != rows[j].TotalSolvedCount {
			return rows[i].TotalSolvedCount > rows[j].TotalSolvedCount
		}
		return strings.ToLower(rows[i].PublicName) < strings.ToLower(rows[j].PublicName)
	})
}
