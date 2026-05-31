package model

import (
	"fmt"
	"sync/atomic"
)

type BackendStatus int32

const (
	StatusHealthy BackendStatus = iota
	StatusUnhealthy
	StatusDraining
)

func (s BackendStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusUnhealthy:
		return "unhealthy"
	case StatusDraining:
		return "draining"
	default:
		return fmt.Sprintf("unknown(%d)", int32(s))
	}
}

type Breaker interface {
	Ready() bool
	Allow() bool
	Observe(failed bool)
}

type Backend struct {
	ID          string
	Addr        string
	Weight      int
	ActiveConns atomic.Int64
	status      atomic.Int32
	breaker     Breaker
}

func NewBackend(addr string, weight, idx int) *Backend {
	if weight <= 0 {
		weight = 1
	}
	return &Backend{
		ID:     fmt.Sprintf("%s#%d", addr, idx),
		Addr:   addr,
		Weight: weight,
	}
}

func (b *Backend) Status() BackendStatus {
	return BackendStatus(b.status.Load())
}

func (b *Backend) Transition(from, to BackendStatus) bool {
	return b.status.CompareAndSwap(int32(from), int32(to))
}

func (b *Backend) Drain() bool {
	for _, from := range []BackendStatus{StatusHealthy, StatusUnhealthy} {
		if b.Transition(from, StatusDraining) {
			return true
		}
	}
	return false
}

func (b *Backend) AttachBreaker(br Breaker) {
	b.breaker = br
}

// Acquire commits a request to this backend, consuming the breaker's single
// half-open probe slot when one is in play. Call it only on the backend the
// balancer actually selected — never while scanning candidates.
func (b *Backend) Acquire() bool {
	if b.breaker == nil {
		return true
	}
	return b.breaker.Allow()
}

func (b *Backend) Observe(failed bool) {
	if b.breaker != nil {
		b.breaker.Observe(failed)
	}
}

// Available is a side-effect-free predicate for balancer scans: the backend is
// healthy and the breaker would admit traffic. It must not consume the probe
// slot — that happens in Acquire on the selected winner.
func (b *Backend) Available() bool {
	return b.Status() == StatusHealthy && (b.breaker == nil || b.breaker.Ready())
}
