package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeInfState(t *testing.T, path string, accounts map[string]informaticsAccountState) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(informaticsStateFile{Version: informaticsStateVersion, Accounts: accounts})
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadInfAccounts(t *testing.T, path string) map[string]informaticsAccountState {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f informaticsStateFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Accounts
}

func infAcc(timedAt ...time.Time) informaticsAccountState {
	timed := make([]TimedSubmission, 0, len(timedAt))
	for _, at := range timedAt {
		timed = append(timed, TimedSubmission{At: at})
	}
	return informaticsAccountState{
		MaxRunID: 100,
		Results:  []TaskResult{{TaskURL: "u", Attempted: true, Solved: true, Timed: timed}},
	}
}

func TestClearInformaticsAllTime(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "inf.json")
	writeInfState(t, path, map[string]informaticsAccountState{
		"1": infAcc(now.AddDate(0, 0, -2)),
		"2": infAcc(now.AddDate(0, -6, 0)),
	})

	n, err := ClearInformaticsCache(path, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 cleared, got %d", n)
	}
	if got := loadInfAccounts(t, path); len(got) != 0 {
		t.Fatalf("expected all accounts gone, got %v", got)
	}
}

func TestClearInformaticsByPeriod(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "inf.json")
	writeInfState(t, path, map[string]informaticsAccountState{
		"recent": infAcc(now.AddDate(0, 0, -2)), // активен последнюю неделю
		"old":    infAcc(now.AddDate(0, -3, 0)), // активность 3 месяца назад
	})

	// Сброс за неделю трогает только "recent".
	n, err := ClearInformaticsCache(path, nil, now.AddDate(0, 0, -7))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleared, got %d", n)
	}
	got := loadInfAccounts(t, path)
	if _, ok := got["recent"]; ok {
		t.Fatal("recent must be cleared")
	}
	if _, ok := got["old"]; !ok {
		t.Fatal("old must remain")
	}
}

func TestClearInformaticsByAccounts(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "inf.json")
	writeInfState(t, path, map[string]informaticsAccountState{
		"111": infAcc(now),
		"222": infAcc(now),
		"333": infAcc(now),
	})

	// Только аккаунты 111 и 333, за всё время.
	n, err := ClearInformaticsCache(path, []string{"111", "333"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 cleared, got %d", n)
	}
	got := loadInfAccounts(t, path)
	if _, ok := got["222"]; !ok || len(got) != 1 {
		t.Fatalf("only 222 must remain, got %v", got)
	}
}

func TestClearInformaticsEmptyAccountsClearsNothing(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "inf.json")
	writeInfState(t, path, map[string]informaticsAccountState{"1": infAcc(now)})

	// Пустой (не nil) список — ученик без informatics-аккаунтов: не трогаем никого.
	n, err := ClearInformaticsCache(path, []string{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("empty filter must clear nothing, got %d", n)
	}
	if len(loadInfAccounts(t, path)) != 1 {
		t.Fatal("account must remain")
	}
}

func TestClearMissingFileNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	if n, err := ClearInformaticsCache(path, nil, time.Time{}); err != nil || n != 0 {
		t.Fatalf("missing file must be no-op: n=%d err=%v", n, err)
	}
	if n, err := ClearCodeforcesCache(path, nil, time.Time{}); err != nil || n != 0 {
		t.Fatalf("missing file must be no-op: n=%d err=%v", n, err)
	}
}

func TestClearCodeforcesCaseInsensitiveHandle(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "cf.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	accounts := map[string]codeforcesAccountState{
		"Tourist": {MaxSubmissionID: 5, Results: []TaskResult{{TaskURL: "u", Timed: []TimedSubmission{{At: now}}}}},
		"petr":    {MaxSubmissionID: 5, Results: []TaskResult{{TaskURL: "u", Timed: []TimedSubmission{{At: now}}}}},
	}
	b, _ := json.Marshal(codeforcesStateFile{Version: codeforcesStateVersion, Accounts: accounts})
	os.WriteFile(path, b, 0o644)

	// Фильтр в другом регистре должен совпасть с ключом "Tourist".
	n, err := ClearCodeforcesCache(path, []string{"tourist"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleared, got %d", n)
	}
	b2, _ := os.ReadFile(path)
	var f codeforcesStateFile
	json.Unmarshal(b2, &f)
	if _, ok := f.Accounts["Tourist"]; ok {
		t.Fatal("Tourist must be cleared regardless of case")
	}
	if _, ok := f.Accounts["petr"]; !ok {
		t.Fatal("petr must remain")
	}
}
