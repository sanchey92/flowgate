package proxyhttp

import (
	"context"
	"errors"
	"net"
	"net/http"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

func mapErrorToStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK

	case errors.Is(err, domainErr.ErrNoRoute):
		return http.StatusNotFound

	case errors.Is(err, domainErr.ErrNoBackends):
		return http.StatusServiceUnavailable

	case errors.Is(err, domainErr.ErrUnknownGroup):
		return http.StatusBadGateway

	case errors.Is(err, domainErr.ErrNilBackend):
		return http.StatusBadGateway

	case errors.Is(err, context.Canceled):
		return http.StatusBadGateway

	default:
		if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
			return http.StatusGatewayTimeout
		}
		return http.StatusBadGateway
	}
}
