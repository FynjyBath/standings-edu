package domain

import "strings"

func NormalizeSite(site string) string {
	return strings.ToLower(strings.TrimSpace(site))
}

func IsValidSlug(slug string) bool {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return false
	}
	if strings.Contains(slug, "/") || strings.Contains(slug, "\\") {
		return false
	}
	return !strings.Contains(slug, "..")
}

func AlphabetLabel(idx int) string {
	if idx < 0 {
		return ""
	}

	label := ""
	for idx >= 0 {
		label = string(rune('A'+(idx%26))) + label
		idx = idx/26 - 1
	}
	return label
}

func ClampScore(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}
