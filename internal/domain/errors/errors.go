package errors

import "errors"

var (
	ErrNoBackends             = errors.New("no backends available")
	ErrProxyStarted           = errors.New("proxy already started")
	ErrProxyStopped           = errors.New("proxy already stopped")
	ErrInvalidProxyProtocol   = errors.New("invalid proxy protocol header")
	ErrUnsupportedProtoFamily = errors.New("unsupported proxy protocol family")
)
