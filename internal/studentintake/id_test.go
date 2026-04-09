package studentintake

import "testing"

func TestGenerateIDFromFullName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fullName string
		want     string
	}{
		{
			name:     "fio in cyrillic",
			fullName: "Иванов Иван Петрович",
			want:     "ivanov-ip",
		},
		{
			name:     "trim and collapse spaces",
			fullName: "  Соловьёв   Артём Юрьевич  ",
			want:     "solovev-ay",
		},
		{
			name:     "mixed symbols",
			fullName: "Петров--Петров Иван",
			want:     "petrov-petrov-i",
		},
		{
			name:     "ascii name",
			fullName: "Smith John Ronald",
			want:     "smith-jr",
		},
		{
			name:     "fallback",
			fullName: "!!!",
			want:     "student",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := GenerateIDFromFullName(tc.fullName)
			if got != tc.want {
				t.Fatalf("GenerateIDFromFullName(%q) = %q, want %q", tc.fullName, got, tc.want)
			}
		})
	}
}

func TestGenerateUniqueID(t *testing.T) {
	t.Parallel()

	taken := map[string]bool{
		"ivanov-ip":   true,
		"ivanov-ip-2": true,
	}

	got := GenerateUniqueID("Иванов Иван Петрович", func(id string) bool {
		return taken[id]
	})
	if got != "ivanov-ip-3" {
		t.Fatalf("GenerateUniqueID() = %q, want %q", got, "ivanov-ip-3")
	}
}

func TestGeneratePublicNameFromFullName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fullName string
		want     string
	}{
		{
			name:     "fio in cyrillic",
			fullName: "Иванов Иван Петрович",
			want:     "Иванов И. П.",
		},
		{
			name:     "trim and collapse spaces",
			fullName: "  Соловьев   Артем Юрьевич  ",
			want:     "Соловьев А. Ю.",
		},
		{
			name:     "single word",
			fullName: "Платон",
			want:     "Платон",
		},
		{
			name:     "empty",
			fullName: "   ",
			want:     "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := GeneratePublicNameFromFullName(tc.fullName)
			if got != tc.want {
				t.Fatalf("GeneratePublicNameFromFullName(%q) = %q, want %q", tc.fullName, got, tc.want)
			}
		})
	}
}
