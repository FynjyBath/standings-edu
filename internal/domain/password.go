package domain

// Пароли доступов хранятся хешем: файлы групп и глобальных доступов лежат на
// диске рядом с остальными данными, попадают в бэкапы и в чужие руки не должны
// отдавать сам пароль.
//
// Формат записи — самоописывающийся, чтобы параметры можно было поднять, не
// ломая старые записи:
//
//	pbkdf2-sha256$<итераций>$<соль base64>$<ключ base64>
//
// PBKDF2-HMAC-SHA256 (RFC 8018) выбран потому, что считается стандартной
// библиотекой: у проекта нет внешних зависимостей, а bcrypt/argon2 живут в
// x/crypto. Реализация сверена с общеизвестными тестовыми векторами
// (см. password_test.go).

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

const (
	passwordHashAlgo = "pbkdf2-sha256"
	// passwordIterations — во сколько раз дорого проверить пароль. Проверка
	// делается при входе (дальше работает кука сессии), поэтому десятки
	// миллисекунд ощутимой цены не имеют, а перебор дорожает на столько же.
	passwordIterations = 210000
	passwordSaltLen    = 16
	passwordKeyLen     = 32
)

// HashPassword превращает пароль в строку для хранения.
func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", fmt.Errorf("пустой пароль")
	}
	salt := make([]byte, passwordSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(plain), salt, passwordIterations, passwordKeyLen)
	return strings.Join([]string{
		passwordHashAlgo,
		strconv.Itoa(passwordIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// IsHashedPassword — строка уже является хешем (а не паролем как есть).
func IsHashedPassword(stored string) bool {
	return strings.HasPrefix(stored, passwordHashAlgo+"$")
}

// VerifyPassword сверяет пароль с сохранённым значением. Незахешированное
// значение сравнивается как есть: так продолжают работать учётки, заведённые до
// перехода на хеши (их пересчитывает миграция).
func VerifyPassword(stored, plain string) bool {
	if stored == "" || plain == "" {
		return false
	}
	if !IsHashedPassword(stored) {
		return subtle.ConstantTimeCompare([]byte(stored), []byte(plain)) == 1
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 4 {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(plain), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// pbkdf2SHA256 — PBKDF2 с HMAC-SHA256 (RFC 8018, раздел 5.2).
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen

	out := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)
	u := make([]byte, 0, hashLen)
	for block := 1; block <= blocks; block++ {
		// U1 = PRF(P, S || INT_32_BE(i))
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf, uint32(block))
		prf.Write(buf)
		u = prf.Sum(u[:0])

		acc := make([]byte, hashLen)
		copy(acc, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range acc {
				acc[i] ^= u[i]
			}
		}
		out = append(out, acc...)
	}
	return out[:keyLen]
}
