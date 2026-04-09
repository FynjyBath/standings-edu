package studentintake

import (
	"sort"
	"strings"

	"standings-edu/internal/domain"
)

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func normalizeSite(site string) string {
	return strings.ToLower(strings.TrimSpace(site))
}

func normalizeStudent(s domain.Student) domain.Student {
	s.ID = strings.TrimSpace(s.ID)
	s.FullName = normalizeWhitespace(s.FullName)
	s.PublicName = normalizeWhitespace(s.PublicName)
	s.Accounts = normalizeAccounts(s.Accounts)
	return s
}

func normalizeAccounts(accounts []domain.Account) []domain.Account {
	if len(accounts) == 0 {
		return nil
	}

	out := make([]domain.Account, 0, len(accounts))
	indexBySite := make(map[string]int, len(accounts))
	for _, a := range accounts {
		site := normalizeSite(a.Site)
		accountID := strings.TrimSpace(a.AccountID)
		if site == "" || accountID == "" {
			continue
		}
		if idx, ok := indexBySite[site]; ok {
			out[idx].AccountID = accountID
			continue
		}
		indexBySite[site] = len(out)
		out = append(out, domain.Account{
			Site:      site,
			AccountID: accountID,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func accountsFromFields(fields map[string]string) []domain.Account {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	accounts := make([]domain.Account, 0, len(keys))
	for _, key := range keys {
		value := fields[key]
		field := strings.TrimSpace(key)
		if field == "" || field == "full_name" || field == "public_name" || field == "id" {
			continue
		}
		accountID := strings.TrimSpace(value)
		if accountID == "" {
			continue
		}
		accounts = append(accounts, domain.Account{
			Site:      normalizeSite(field),
			AccountID: accountID,
		})
	}
	return normalizeAccounts(accounts)
}
