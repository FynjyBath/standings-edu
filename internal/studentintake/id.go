package studentintake

import (
	"fmt"
	"strings"
)

var translitTable = map[rune]string{
	'а': "a",
	'б': "b",
	'в': "v",
	'г': "g",
	'д': "d",
	'е': "e",
	'ё': "e",
	'ж': "zh",
	'з': "z",
	'и': "i",
	'й': "y",
	'к': "k",
	'л': "l",
	'м': "m",
	'н': "n",
	'о': "o",
	'п': "p",
	'р': "r",
	'с': "s",
	'т': "t",
	'у': "u",
	'ф': "f",
	'х': "h",
	'ц': "ts",
	'ч': "ch",
	'ш': "sh",
	'щ': "sch",
	'ъ': "",
	'ы': "y",
	'ь': "",
	'э': "e",
	'ю': "yu",
	'я': "ya",
}

func GenerateIDFromFullName(fullName string) string {
	parts := strings.Fields(normalizeWhitespace(fullName))
	if len(parts) == 0 {
		return "student"
	}

	base := slugifyASCII(transliterate(parts[0]))
	if base == "" {
		base = "student"
	}

	initials := make([]string, 0, 2)
	if len(parts) > 1 {
		if initial := firstInitial(parts[1]); initial != "" {
			initials = append(initials, initial)
		}
	}
	if len(parts) > 2 {
		if initial := firstInitial(parts[2]); initial != "" {
			initials = append(initials, initial)
		}
	}

	id := base
	if len(initials) > 0 {
		id = base + "-" + strings.Join(initials, "")
	}

	id = slugifyASCII(id)
	if id == "" {
		return "student"
	}
	return id
}

func GenerateUniqueID(fullName string, isTaken func(id string) bool) string {
	base := GenerateIDFromFullName(fullName)
	if !isTaken(base) {
		return base
	}

	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !isTaken(candidate) {
			return candidate
		}
	}
}

func transliterate(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if mapped, ok := translitTable[r]; ok {
			b.WriteString(mapped)
			continue
		}
		if isASCIIAlphaNum(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	return b.String()
}

func firstInitial(part string) string {
	s := transliterate(part)
	for _, r := range s {
		if isASCIIAlphaNum(r) {
			return string(r)
		}
	}
	return ""
}

func slugifyASCII(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := true

	for _, r := range s {
		if isASCIIAlphaNum(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(b.String(), "-")
}

func isASCIIAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}
