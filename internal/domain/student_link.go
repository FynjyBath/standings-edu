package domain

// Ссылка на страницу ученика для родителей: /standings/child?key=<ключ>.
//
// Ключ не хранится, а вычисляется из id ученика по секрету сервера:
//
//	ключ = первые 128 бит HMAC-SHA256(секрет, "student-link:v1:" + id), hex
//
// Поэтому ссылка у ученика есть сразу, как только он заведён, и не меняется,
// пока не меняются его id и секрет. Без секрета её не вычислить и не подобрать,
// а id в ней не виден — по ссылке одного ребёнка не найти остальных. Префикс
// разводит этот ключ с любым другим применением того же секрета.
//
// Обратная сторона: отозвать ссылку одного ученика нельзя — только сменить
// секрет, и тогда поменяются ссылки у всех.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// studentLinkKeyBytes — длина ключа в байтах (128 бит, 32 hex-символа).
const studentLinkKeyBytes = 16

// MinStudentLinkSecretBytes — минимальная длина секрета: короче 128 бит
// ссылки становится реально перебрать.
const MinStudentLinkSecretBytes = 16

// StudentLinkKey — ключ ссылки ученика. Пустой секрет или id — пустой ключ
// (ссылок нет).
func StudentLinkKey(secret []byte, studentID string) string {
	if len(secret) == 0 || studentID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("student-link:v1:" + studentID))
	return hex.EncodeToString(mac.Sum(nil)[:studentLinkKeyBytes])
}
