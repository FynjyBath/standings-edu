package standings

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
)

// timedSites — сайты, отдающие время посылок (лента и метрики скорости строятся
// только по ним; ACMP времени не даёт).
var timedSites = map[string]bool{"codeforces": true, "informatics": true}

var moscowZone = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*60*60)
	}
	return loc
}()

const (
	// profileRecentLimit — предохранитель от патологически огромных профилей;
	// в норме в ленту попадают ВСЕ посылки ученика (по флагу 🚩 к ним можно
	// перейти фильтром эпизода, лента свёрнута до 30 строк кнопкой).
	profileRecentLimit = 5000
	profileDailyDays   = 90
)

// buildStudentProfiles собирает профили активности учеников из уже скачанных
// статусов (без позиций в группах — их добавляет pipeline). now — момент
// генерации (UTC), от него считаются окна 7/30 дней и график активности.
func (b *Builder) buildStudentProfiles(students []domain.Student, statusByStudent map[string]*accountStatuses, now time.Time) map[string]*domain.GeneratedStudentProfile {
	out := make(map[string]*domain.GeneratedStudentProfile, len(students))
	for _, student := range students {
		st := statusByStudent[student.ID]
		if st == nil {
			st = newAccountStatuses()
		}
		out[student.ID] = b.buildStudentProfile(student, st, now)
	}
	return out
}

func (b *Builder) buildStudentProfile(student domain.Student, st *accountStatuses, now time.Time) *domain.GeneratedStudentProfile {
	profile := &domain.GeneratedStudentProfile{
		StudentID:  student.ID,
		PublicName: student.PublicName,
		FullName:   student.FullName,
		Accounts:   student.Accounts,
	}

	siteOf := func(taskURL string) string {
		if site, _, ok := b.sources.ResolveSiteByTaskURL(taskURL); ok {
			return domain.NormalizeSite(site)
		}
		return "other"
	}

	// Лента посылок (по сайтам со временем).
	timeline := make([]domain.StudentSubmission, 0)
	submissionsBySite := make(map[string]int)
	for taskURL, subs := range st.timed {
		site := siteOf(taskURL)
		submissionsBySite[site] += len(subs)
		for _, sub := range subs {
			timeline = append(timeline, domain.StudentSubmission{
				At:      sub.At,
				Site:    site,
				TaskURL: taskURL,
				Label:   taskLabel(site, taskURL),
				Solved:  sub.Solved,
				Score:   sub.Score,
			})
		}
	}
	sort.Slice(timeline, func(i, j int) bool { return timeline[i].At.After(timeline[j].At) })

	// Счётчики по сайтам: решено/затронуто из множеств, посылки из timeline.
	solvedBySite := make(map[string]int)
	attemptedBySite := make(map[string]int)
	for taskURL := range st.solved {
		solvedBySite[siteOf(taskURL)]++
	}
	for taskURL := range st.attempted {
		attemptedBySite[siteOf(taskURL)]++
	}
	siteNames := make([]string, 0)
	seenSite := make(map[string]struct{})
	for _, m := range []map[string]int{solvedBySite, attemptedBySite, submissionsBySite} {
		for site := range m {
			if _, ok := seenSite[site]; !ok {
				seenSite[site] = struct{}{}
				siteNames = append(siteNames, site)
			}
		}
	}
	sort.Strings(siteNames)
	for _, site := range siteNames {
		profile.Sites = append(profile.Sites, domain.StudentSiteStat{
			Site:        site,
			Solved:      solvedBySite[site],
			Attempted:   attemptedBySite[site],
			Submissions: submissionsBySite[site],
			HasTimes:    timedSites[site],
		})
	}

	// Метрики скорости: по задачам, решённым на сайтах со временем. Для каждой
	// задачи — число посылок до первого «принято» (i+1); первая попытка = i==0.
	sumAttempts := 0
	todayMSK := now.In(moscowZone).Format("2006-01-02")
	for _, subs := range st.timed {
		ordered := append([]source.TimedSubmission(nil), subs...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
		for i, sub := range ordered {
			if sub.Solved {
				profile.Stats.SolvedWithTimes++
				sumAttempts += i + 1
				if i == 0 {
					profile.Stats.FirstTrySolved++
				}
				// Задача, решённая впервые сегодня (по календарю MSK).
				if sub.At.In(moscowZone).Format("2006-01-02") == todayMSK {
					profile.Stats.SolvedToday++
				}
				break
			}
		}
	}
	if profile.Stats.SolvedWithTimes > 0 {
		profile.Stats.AvgAttemptsToSolve = roundOne(float64(sumAttempts) / float64(profile.Stats.SolvedWithTimes))
	}

	// Агрегаты активности.
	profile.Stats.TotalSolved = len(st.solved)
	profile.Stats.TotalAttempted = len(st.attempted)
	profile.Stats.TotalSubmissions = len(timeline)
	cutoff7 := now.Add(-7 * 24 * time.Hour)
	cutoff30 := now.Add(-30 * 24 * time.Hour)
	activeDays := make(map[string]struct{})
	dayCounts := make(map[string]int)
	for _, sub := range timeline {
		if sub.At.After(cutoff7) {
			profile.Stats.Submissions7d++
		}
		if sub.At.After(cutoff30) {
			profile.Stats.Submissions30d++
		}
		day := sub.At.In(moscowZone).Format("2006-01-02")
		activeDays[day] = struct{}{}
		dayCounts[day]++
	}
	profile.Stats.ActiveDays = len(activeDays)
	if len(timeline) > 0 {
		last := timeline[0].At
		profile.Stats.LastActivity = &last
	}

	// График активности: последние N дней (МSK), включая нули (слева направо).
	today := now.In(moscowZone)
	for d := profileDailyDays - 1; d >= 0; d-- {
		day := today.AddDate(0, 0, -d).Format("2006-01-02")
		profile.DailyActivity = append(profile.DailyActivity, domain.StudentDayCount{Date: day, Count: dayCounts[day]})
	}

	if len(timeline) > profileRecentLimit {
		timeline = timeline[:profileRecentLimit]
	}
	profile.Recent = timeline
	return profile
}

func roundOne(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

// addGroupPositions добавляет в профили участников их позицию в группе — по
// доске почёта (решено) и по оценкам, если настроены. Место — стандартный ранг
// (1 + число строго выше), ничьи делят место.
func addGroupPositions(profiles map[string]*domain.GeneratedStudentProfile, group domain.GroupDefinition, std domain.GeneratedGroupStandings) {
	// Место на доске почёта — по той же «Сумме» (решено на сайтах страницы), что
	// и порядок в самой доске, иначе место не совпадёт с позицией в таблице.
	honorByStudent := make(map[string]int, len(std.SolvedSummary))
	honorCounts := make([]int, 0, len(std.SolvedSummary))
	for _, row := range std.SolvedSummary {
		honorByStudent[row.StudentID] = row.SolvedCountOnPageSites
		honorCounts = append(honorCounts, row.SolvedCountOnPageSites)
	}
	honorTotal := len(std.SolvedSummary)

	gradeByStudent := make(map[string]float64)
	gradeFinals := make([]float64, 0)
	if std.Grades != nil {
		for _, row := range std.Grades.Rows {
			if row.Final != nil {
				gradeByStudent[row.StudentID] = *row.Final
				gradeFinals = append(gradeFinals, *row.Final)
			}
		}
	}

	title := strings.TrimSpace(group.Title)
	if title == "" {
		title = group.Slug
	}

	for _, studentID := range domain.NormalizeGroups(group.StudentIDs) {
		profile, ok := profiles[studentID]
		if !ok {
			continue
		}
		pos := domain.StudentGroupStanding{Slug: group.Slug, Title: title}
		if sc, ok := honorByStudent[studentID]; ok {
			pos.SolvedCount = sc
			pos.HonorTotal = honorTotal
			place := 1
			for _, c := range honorCounts {
				if c > sc {
					place++
				}
			}
			pos.HonorPlace = place
		}
		if g, ok := gradeByStudent[studentID]; ok {
			gg := g
			pos.Grade = &gg
			pos.GradeTotal = len(gradeFinals)
			place := 1
			for _, f := range gradeFinals {
				if f > g {
					place++
				}
			}
			pos.GradePlace = place
		}
		profile.Groups = append(profile.Groups, pos)
	}
}

var (
	cfProblemRe   = regexp.MustCompile(`/(?:problemset/problem|contest|gym)/(\d+)(?:/problem)?/([A-Za-z0-9]+)`)
	informaticsRe = regexp.MustCompile(`chapterid=(\d+)`)
	acmpRe        = regexp.MustCompile(`id_task=(\d+)`)
)

// taskLabel — короткая метка задачи для ленты («CF 1711A», «Инф 1791», «ACMP 29»).
func taskLabel(site, taskURL string) string {
	switch site {
	case "codeforces":
		if m := cfProblemRe.FindStringSubmatch(taskURL); m != nil {
			return "CF " + m[1] + strings.ToUpper(m[2])
		}
		return "CF"
	case "informatics":
		if m := informaticsRe.FindStringSubmatch(taskURL); m != nil {
			return "Инф " + m[1]
		}
		return "Информатикс"
	case "acmp":
		if m := acmpRe.FindStringSubmatch(taskURL); m != nil {
			return "ACMP " + m[1]
		}
		return "ACMP"
	}
	if u, err := url.Parse(taskURL); err == nil && u.Host != "" {
		return u.Host
	}
	return fmt.Sprintf("%.40s", taskURL)
}
