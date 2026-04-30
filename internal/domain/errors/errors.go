package errors

import "errors"

var (
	ErrNoBackends   = errors.New("no backends available")
	ErrProxyStarted = errors.New("proxy already started")
	ErrProxyStopped = errors.New("proxy already stopped")
)
