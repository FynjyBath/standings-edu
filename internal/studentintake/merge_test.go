package studentintake

import (
	"testing"

	"standings-edu/internal/domain"
)

func TestMergeStudentsExistingStudent(t *testing.T) {
	t.Parallel()

	existing := []domain.Student{
		{
			ID:         "admin-id",
			FullName:   "Иванов Иван Иванович",
			PublicName: "Иванов И. И.",
			Accounts: []domain.Account{
				{Site: "codeforces", AccountID: "old_cf"},
			},
		},
	}
	intake := []domain.Student{
		{
			ID:       "ivanov-ii",
			FullName: "Иванов Иван Иванович",
			Accounts: []domain.Account{
				{Site: "codeforces", AccountID: "new_cf"},
				{Site: "acmp", AccountID: "777"},
			},
		},
	}

	merged, stats, err := MergeStudents(existing, intake)
	if err != nil {
		t.Fatalf("MergeStudents() error = %v", err)
	}
	if stats.Updated != 1 || stats.Added != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(merged) != 1 {
		t.Fatalf("len(merged) = %d, want 1", len(merged))
	}
	if merged[0].ID != "admin-id" {
		t.Fatalf("merged[0].ID = %q, want %q", merged[0].ID, "admin-id")
	}
	if got := accountIDBySite(merged[0].Accounts, "codeforces"); got != "new_cf" {
		t.Fatalf("codeforces account = %q, want %q", got, "new_cf")
	}
	if got := accountIDBySite(merged[0].Accounts, "acmp"); got != "777" {
		t.Fatalf("acmp account = %q, want %q", got, "777")
	}
}

func TestMergeStudentsNewStudent(t *testing.T) {
	t.Parallel()

	existing := []domain.Student{
		{
			ID:       "admin-id",
			FullName: "Иванов Иван Иванович",
		},
	}
	intake := []domain.Student{
		{
			FullName: "Петров Петр Петрович",
			Accounts: []domain.Account{
				{Site: "codeforces", AccountID: "petrov_cf"},
			},
		},
	}

	merged, stats, err := MergeStudents(existing, intake)
	if err != nil {
		t.Fatalf("MergeStudents() error = %v", err)
	}
	if stats.Updated != 0 || stats.Added != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
	if merged[1].ID != "petrov-pp" {
		t.Fatalf("merged[1].ID = %q, want %q", merged[1].ID, "petrov-pp")
	}
	if merged[1].FullName != "Петров Петр Петрович" {
		t.Fatalf("merged[1].FullName = %q", merged[1].FullName)
	}
}

func TestMergeStudentsSiteAccounts(t *testing.T) {
	t.Parallel()

	existing := []domain.Student{
		{
			ID:         "admin-id",
			FullName:   "Иванов Иван Иванович",
			PublicName: "Иванов И. И.",
			Accounts: []domain.Account{
				{Site: "codeforces", AccountID: "old_cf"},
				{Site: "acmp", AccountID: "123"},
			},
		},
	}
	intake := []domain.Student{
		{
			FullName: "Иванов Иван Иванович",
			Accounts: []domain.Account{
				{Site: "codeforces", AccountID: "new_cf"},
				{Site: "informatics", AccountID: "999"},
			},
		},
	}

	merged, _, err := MergeStudents(existing, intake)
	if err != nil {
		t.Fatalf("MergeStudents() error = %v", err)
	}

	if got := accountIDBySite(merged[0].Accounts, "codeforces"); got != "new_cf" {
		t.Fatalf("codeforces account = %q, want %q", got, "new_cf")
	}
	if got := accountIDBySite(merged[0].Accounts, "acmp"); got != "123" {
		t.Fatalf("acmp account = %q, want %q", got, "123")
	}
	if got := accountIDBySite(merged[0].Accounts, "informatics"); got != "999" {
		t.Fatalf("informatics account = %q, want %q", got, "999")
	}
}

func TestMergeStudentsIgnoreEmptyFields(t *testing.T) {
	t.Parallel()

	existing := []domain.Student{
		{
			ID:         "admin-id",
			FullName:   "Иванов Иван Иванович",
			PublicName: "Иванов И. И.",
			Accounts: []domain.Account{
				{Site: "codeforces", AccountID: "old_cf"},
			},
		},
	}
	intake := []domain.Student{
		{
			FullName:   "  Иванов   Иван Иванович  ",
			PublicName: "   ",
			Accounts: []domain.Account{
				{Site: "codeforces", AccountID: "   "},
				{Site: "informatics", AccountID: " 321 "},
			},
		},
	}

	merged, _, err := MergeStudents(existing, intake)
	if err != nil {
		t.Fatalf("MergeStudents() error = %v", err)
	}

	if merged[0].PublicName != "Иванов И. И." {
		t.Fatalf("PublicName changed to %q, want unchanged", merged[0].PublicName)
	}
	if got := accountIDBySite(merged[0].Accounts, "codeforces"); got != "old_cf" {
		t.Fatalf("codeforces account = %q, want unchanged %q", got, "old_cf")
	}
	if got := accountIDBySite(merged[0].Accounts, "informatics"); got != "321" {
		t.Fatalf("informatics account = %q, want %q", got, "321")
	}
}

func accountIDBySite(accounts []domain.Account, site string) string {
	for _, account := range accounts {
		if account.Site == site {
			return account.AccountID
		}
	}
	return ""
}
