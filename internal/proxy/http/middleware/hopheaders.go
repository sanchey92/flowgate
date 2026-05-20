package middleware

import "net/http"

// hopByHopHeaders — заголовки уровня соединения (RFC 9110 §7.6.1,
// исторически RFC 7230 §6.1). Прокси не переносит их между хопами.
//
// Список дословно повторяет hopHeaders из net/http/httputil: ReverseProxy
// уже снимает эти заголовки и с исходящего запроса, и с ответа апстрима ДО
// того, как до них доберётся наш код (см. Stage C, раздел 13). Поэтому здесь
// список нужен ровно для одной задачи — не дать кастомному правилу заголовков
// случайно вернуть hop-by-hop обратно.
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {}, // не из RFC, но де-факто стандарт; есть и в stdlib
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {}, // канонизированная форма "TE"
	"Trailer":             {}, // именно Trailer, не "Trailers" — см. ниже
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

// isHopByHop сообщает, является ли name (в любом регистре) hop-by-hop.
func isHopByHop(name string) bool {
	_, ok := hopByHopHeaders[http.CanonicalHeaderKey(name)]
	return ok
}
