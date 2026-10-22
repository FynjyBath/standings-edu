package domain

import (
	"encoding/hex"
	"testing"
)

// Ссылки уже розданы родителям: формат ключа менять нельзя. Значения посчитаны
// независимо (Python: hmac.new(secret, b"student-link:v1:"+id, sha256),
// первые 16 байт в hex) — тест падает на любой смене схемы.
func TestStudentLinkKeyIsStable(t *testing.T) {
	secret, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	cases := map[string]string{
		"ivanov-ii":  "9cee3d4dc9ca4681802031799a3dac71",
		"petrova-aa": "2bb91c4f804ea2693391e9c1f2e67210",
	}
	for id, want := range cases {
		if got := StudentLinkKey(secret, id); got != want {
			t.Errorf("StudentLinkKey(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestStudentLinkKeyProperties(t *testing.T) {
	a := []byte("0123456789abcdef0123456789abcdef")
	b := []byte("fedcba9876543210fedcba9876543210")
	if StudentLinkKey(a, "s1") != StudentLinkKey(a, "s1") {
		t.Error("ключ должен быть детерминированным")
	}
	if StudentLinkKey(a, "s1") == StudentLinkKey(a, "s2") {
		t.Error("у разных учеников ключи должны различаться")
	}
	if StudentLinkKey(a, "s1") == StudentLinkKey(b, "s1") {
		t.Error("другой секрет — другой ключ")
	}
	if got := StudentLinkKey(a, "s1"); len(got) != 32 {
		t.Errorf("ключ — 32 hex-символа, получено %q", got)
	}
	if StudentLinkKey(nil, "s1") != "" || StudentLinkKey(a, "") != "" {
		t.Error("без секрета или id ключа нет")
	}
}

// «Наблюдатель» (а с ним «Жюри» и «Админ») видит ссылки для родителей.
func TestParentLinksPermInPresets(t *testing.T) {
	for name, perms := range map[string][]Perm{
		"observer": ObserverPerms(), "jury": JuryPerms(), "admin": AdminPerms(),
	} {
		if !NewPermSet(perms...).Has(PermViewParentLinks) {
			t.Errorf("пресет %s должен включать %s", name, PermViewParentLinks)
		}
	}
	if !KnownPerm(PermViewParentLinks) {
		t.Error("право должно быть в каталоге")
	}
}
