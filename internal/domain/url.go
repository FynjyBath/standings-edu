package domain

import (
	"net/url"
	"strings"
)

func NormalizeTaskURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	u, err := url.Parse(s)
	if err != nil {
		return s
	}

	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""

	if u.Path != "/" {
		u.Path = strings.TrimRight(u.Path, "/")
		if u.Path == "" {
			u.Path = "/"
		}
	}

	return u.String()
}
