package source

import (
	"testing"
	"time"
)

// create_time от informatics — момент в UTC: сейчас RFC3339 с "+00:00",
// наивные форматы (если формат сменят) тоже интерпретируются как UTC.
func TestParseInformaticsTime(t *testing.T) {
	want := time.Date(2026, 7, 5, 5, 28, 32, 0, time.UTC)

	cases := []string{
		"2026-07-05T05:28:32+00:00", // текущий формат API
		"2026-07-05T05:28:32Z",
		"2026-07-05T08:28:32+03:00", // явный сдвиг — тот же момент
		"2026-07-05T05:28:32",       // наивный ISO → UTC
		"2026-07-05 05:28:32",       // наивный с пробелом → UTC
	}
	for _, raw := range cases {
		got, ok := parseInformaticsTime(raw)
		if !ok || !got.Equal(want) {
			t.Fatalf("parse %q: got %v ok=%v, want %v", raw, got, ok, want)
		}
	}

	for _, raw := range []string{"", "вчера", "05:28:32"} {
		if _, ok := parseInformaticsTime(raw); ok {
			t.Fatalf("parse %q must fail", raw)
		}
	}
}
