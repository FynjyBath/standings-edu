package domain

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// PBKDF2-HMAC-SHA256 сверяем с общеизвестными векторами: своя реализация
// криптографии обязана совпадать с чужой до байта.
func TestPBKDF2SHA256Vectors(t *testing.T) {
	cases := []struct {
		password, salt string
		iter, keyLen   int
		want           string
	}{
		{"password", "salt", 1, 32, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{"password", "salt", 2, 32, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{"password", "salt", 4096, 32, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
		{"passwordPASSWORDpassword", "saltSALTsaltSALTsaltSALTsaltSALTsalt", 4096, 40,
			"348c89dbcbd32b2f32d814b8116e84cf2b17347ebc1800181c4e2a1fb8dd53e1c635518c7dac47e9"},
	}
	for _, c := range cases {
		got := hex.EncodeToString(pbkdf2SHA256([]byte(c.password), []byte(c.salt), c.iter, c.keyLen))
		if got != c.want {
			t.Errorf("pbkdf2(%q, %q, %d, %d):\n got %s\nwant %s", c.password, c.salt, c.iter, c.keyLen, got, c.want)
		}
	}
}

// Хеш проверяется своим паролем и не проверяется чужим; соль делает записи
// разными даже для одинаковых паролей.
func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("тайна-123")
	if err != nil {
		t.Fatal(err)
	}
	if !IsHashedPassword(hash) || strings.Contains(hash, "тайна-123") {
		t.Fatalf("хеш не должен содержать пароль: %s", hash)
	}
	if !VerifyPassword(hash, "тайна-123") {
		t.Error("свой пароль должен проходить")
	}
	for _, wrong := range []string{"тайна-124", "", "тайна-123 ", hash} {
		if VerifyPassword(hash, wrong) {
			t.Errorf("чужой пароль прошёл: %q", wrong)
		}
	}
	other, err := HashPassword("тайна-123")
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Error("две записи одного пароля должны отличаться солью")
	}
	if _, err := HashPassword(""); err == nil {
		t.Error("пустой пароль хешировать нечего")
	}
}

// Незахешированное значение сверяется как есть: учётки, заведённые до перехода
// на хеши, продолжают работать до миграции.
func TestVerifyPasswordLegacyPlaintext(t *testing.T) {
	if !VerifyPassword("старый-пароль", "старый-пароль") {
		t.Error("старый пароль как есть должен проходить")
	}
	if VerifyPassword("старый-пароль", "другой") {
		t.Error("чужой пароль не должен проходить")
	}
	if VerifyPassword("", "") || VerifyPassword("x", "") {
		t.Error("пустое не проходит")
	}
	// Битый хеш не должен «проходить» как plaintext-сравнение.
	if VerifyPassword("pbkdf2-sha256$нечисло$..$..", "что угодно") {
		t.Error("битый хеш проходить не должен")
	}
}

// Проверка пароля должна быть заметно дорогой (иначе перебор дёшев), но не
// настолько, чтобы вход подвисал.
func TestHashPasswordCost(t *testing.T) {
	hash, err := HashPassword("bench")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	VerifyPassword(hash, "bench")
	elapsed := time.Since(start)
	t.Logf("проверка пароля: %v", elapsed)
	if elapsed < 10*time.Millisecond {
		t.Errorf("слишком дёшево (%v): перебор будет быстрым", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("слишком дорого (%v): вход будет подвисать", elapsed)
	}
}
