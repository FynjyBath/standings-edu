package source

import (
	"strings"

	"standings-edu/internal/domain"
)

// matchNamesToStudents сопоставляет строки-имена («Имя Фамилия», «Фамилия Имя
// Отчество» — порядок не важен) ученикам standings по ФИО. Сравнение пословное:
// каждый токен имени должен найтись среди токенов ФИО ученика (инициал «я.»
// совпадает с «яна», но ценится слабо). При равном качестве совпадения у двух
// учеников имя остаётся несопоставленным (тёзки — безопаснее показать отдельной
// строкой, чем приписать чужую оценку). Возвращает индекс имени -> student_id.
func matchNamesToStudents(names []string, students []domain.Student) map[int]string {
	type candidate struct {
		id     string
		tokens []string
	}
	candidates := make([]candidate, 0, len(students))
	for _, s := range students {
		tokens := uniqueStrings(append(
			strings.Fields(normalizeForMatch(s.FullName)),
			strings.Fields(normalizeForMatch(s.PublicName))...,
		))
		if len(tokens) == 0 {
			continue
		}
		candidates = append(candidates, candidate{id: s.ID, tokens: tokens})
	}

	type claim struct {
		idx   int
		score int
	}
	out := make(map[int]string)
	taken := make(map[string]claim) // studentID -> лучшая строка
	for idx, name := range names {
		nameTokens := strings.Fields(normalizeForMatch(name))
		if len(nameTokens) < 2 {
			continue
		}
		bestID := ""
		bestScore := 0
		tie := false
		for _, c := range candidates {
			score, ok := nameMatchScore(nameTokens, c.tokens)
			if !ok {
				continue
			}
			switch {
			case score > bestScore:
				bestScore, bestID, tie = score, c.id, false
			case score == bestScore && bestScore > 0 && c.id != bestID:
				tie = true
			}
		}
		if bestID == "" || tie {
			continue
		}
		// Один ученик — одна строка: при повторном совпадении побеждает более
		// качественное (полное) совпадение.
		if prev, ok := taken[bestID]; ok {
			if bestScore <= prev.score {
				continue
			}
			delete(out, prev.idx)
		}
		taken[bestID] = claim{idx: idx, score: bestScore}
		out[idx] = bestID
	}
	return out
}

// nameMatchScore — качество совпадения имени с токенами ФИО ученика: каждый
// токен имени должен совпасть со своим токеном ФИО, каждый токен ФИО
// используется один раз. Точное совпадение слова даёт его длину в очках,
// совпадение по инициалу («я.» ↔ «яна») — только 1 очко: инициал — слабое
// свидетельство и не должен перебивать настоящую фамилию другого ученика.
// Требуется минимум два точных совпадения слов; если полных слов (не инициалов)
// у ученика или в имени меньше — по их числу, но хотя бы одно (случай ученика
// только с public name «Петров В.»).
func nameMatchScore(nameTokens, studentTokens []string) (int, bool) {
	isInitial := func(s string) bool {
		r := []rune(s)
		return len(r) == 2 && r[1] == '.'
	}

	used := make([]bool, len(studentTokens))
	score := 0
	fullMatches := 0
	for _, nt := range nameTokens {
		matched := false
		// Сначала точное слово, потом инициал — чтобы инициал не «съел» токен,
		// который мог совпасть точно.
		if !isInitial(nt) {
			for i, st := range studentTokens {
				if !used[i] && nt == st {
					used[i] = true
					matched = true
					score += len([]rune(nt))
					fullMatches++
					break
				}
			}
		}
		if !matched {
			for i, st := range studentTokens {
				if !used[i] && initialTokenMatch(nt, st) {
					used[i] = true
					matched = true
					score++
					break
				}
			}
		}
		if !matched {
			return 0, false
		}
	}

	studentFull := 0
	for _, st := range studentTokens {
		if !isInitial(st) {
			studentFull++
		}
	}
	nameFull := 0
	for _, nt := range nameTokens {
		if !isInitial(nt) {
			nameFull++
		}
	}
	required := 2
	if nameFull < required {
		required = nameFull
	}
	if studentFull < required {
		required = studentFull
	}
	if required < 1 {
		required = 1
	}
	if len(nameTokens) < 2 || fullMatches < required {
		return 0, false
	}
	return score, true
}

// initialTokenMatch — совпадение инициала с полным словом в любую сторону
// («я.» ↔ «яна»); пара одинаковых инициалов тоже считается (слабым) совпадением.
func initialTokenMatch(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	isInitial := func(r []rune) bool { return len(r) == 2 && r[1] == '.' }
	switch {
	case isInitial(ra) && isInitial(rb):
		return ra[0] == rb[0]
	case isInitial(ra) && len(rb) > 1:
		return ra[0] == rb[0]
	case isInitial(rb) && len(ra) > 1:
		return rb[0] == ra[0]
	}
	return false
}
