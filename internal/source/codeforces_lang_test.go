package source

import (
	"context"
	"net/url"
	"strconv"
	"testing"
)

// Все запросы к Codeforces получают lang=ru, чтобы названия задач/контестов
// приходили на русском (где есть перевод). Проверяем и без ключей, и с ключами
// (lang должен войти в подпись — apiSig присутствует, запрос валиден).
func TestCodeforcesRequestsAskRussian(t *testing.T) {
	check := func(c *CodeforcesAPIClient, wantSig bool) {
		q := make(url.Values)
		q.Set("contestId", strconv.Itoa(42))
		req, err := c.newAPIRequest(context.Background(), "contest.standings", q)
		if err != nil {
			t.Fatalf("newAPIRequest: %v", err)
		}
		got := req.URL.Query()
		if got.Get("lang") != "ru" {
			t.Fatalf("lang must be ru, got %q (query=%s)", got.Get("lang"), req.URL.RawQuery)
		}
		if wantSig && got.Get("apiSig") == "" {
			t.Fatalf("signed request must carry apiSig: %s", req.URL.RawQuery)
		}
	}

	// Без ключей (анонимно).
	check(NewCodeforcesAPIClient(), false)

	// С ключами — lang попадает в подпись, apiSig проставляется.
	signed := NewCodeforcesAPIClient()
	signed.apiKey = "key"
	signed.apiSecret = "secret"
	check(signed, true)
}

// Явно заданный lang не перетирается (на будущее — если где-то попросят en).
func TestCodeforcesLangNotOverridden(t *testing.T) {
	c := NewCodeforcesAPIClient()
	q := make(url.Values)
	q.Set("lang", "en")
	req, err := c.newAPIRequest(context.Background(), "contest.status", q)
	if err != nil {
		t.Fatalf("newAPIRequest: %v", err)
	}
	if got := req.URL.Query().Get("lang"); got != "en" {
		t.Fatalf("explicit lang must be kept, got %q", got)
	}
}
