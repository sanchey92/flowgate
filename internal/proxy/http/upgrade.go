package proxyhttp

import (
	"net/http"
	"strings"
)

func isWebSocketUpgrade(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}

	if !isHeaderContainsToken(r.Header.Values("Connection"), "upgrade") {
		return false
	}

	return strings.EqualFold(r.Header.Get("Upgrade"), upgradeWebSocket)
}

func isHeaderContainsToken(values []string, target string) bool {
	for _, v := range values {
		start := 0

		for i := 0; i <= len(v); i++ {
			if i != len(v) && v[i] != ',' {
				continue
			}

			token := strings.TrimSpace(v[start:i])

			if strings.EqualFold(token, target) {
				return true
			}

			start = i + 1
		}
	}
	return false
}
