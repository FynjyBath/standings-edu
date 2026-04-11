package domain

import "strings"

func NormalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func NormalizeID(id string) string {
	return strings.TrimSpace(id)
}

func NormalizeAccounts(accounts []Account) []Account {
	if len(accounts) == 0 {
		return nil
	}

	out := make([]Account, 0, len(accounts))
	indexBySite := make(map[string]int, len(accounts))
	for _, account := range accounts {
		site := NormalizeSite(account.Site)
		accountID := strings.TrimSpace(account.AccountID)
		if site == "" || accountID == "" {
			continue
		}
		if idx, ok := indexBySite[site]; ok {
			out[idx].AccountID = accountID
			continue
		}
		indexBySite[site] = len(out)
		out = append(out, Account{Site: site, AccountID: accountID})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func MergeAccounts(existing []Account, updates []Account) []Account {
	if len(updates) == 0 {
		return NormalizeAccounts(existing)
	}

	merged := make([]Account, 0, len(existing)+len(updates))
	indexBySite := make(map[string]int, len(existing)+len(updates))
	for _, account := range NormalizeAccounts(existing) {
		indexBySite[account.Site] = len(merged)
		merged = append(merged, account)
	}
	for _, update := range NormalizeAccounts(updates) {
		if idx, ok := indexBySite[update.Site]; ok {
			merged[idx].AccountID = update.AccountID
			continue
		}
		indexBySite[update.Site] = len(merged)
		merged = append(merged, update)
	}
	return merged
}

func NormalizeGroups(groups []string) []string {
	if len(groups) == 0 {
		return nil
	}

	out := make([]string, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		out = append(out, group)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func MergeGroups(existing []string, updates []string) []string {
	return NormalizeGroups(append(NormalizeGroups(existing), NormalizeGroups(updates)...))
}

func NormalizeStudent(student Student) Student {
	student.ID = NormalizeID(student.ID)
	student.FullName = NormalizeWhitespace(student.FullName)
	student.PublicName = NormalizeWhitespace(student.PublicName)
	student.Accounts = NormalizeAccounts(student.Accounts)
	student.Groups = NormalizeGroups(student.Groups)
	return student
}

func NormalizeStudents(students []Student) []Student {
	out := make([]Student, len(students))
	for i := range students {
		out[i] = NormalizeStudent(students[i])
	}
	return out
}
