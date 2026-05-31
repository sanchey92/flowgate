package passive

import (
	"sync/atomic"
	"time"
)

type State int32

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

type Breaker struct {
	threshold int64
	recovery  time.Duration
	window    *windowCounter

	state     atomic.Int32
	openedAt  atomic.Int64
	lastProbe atomic.Int64
}

func NewBreaker(cfg *Config) *Breaker {
	cfg = cfg.withDefaults()
	return &Breaker{
		threshold: int64(cfg.ErrorThreshold),
		recovery:  cfg.RecoveryInterval,
		window:    newWindowCounter(cfg.Window, windowBuckets),
	}
}

// Ready reports, without side effects, whether the balancer may consider this
// backend: closed always; open once recovery has elapsed; half-open while a
// probe slot is free. The probe is consumed by Allow on the selected backend.
func (b *Breaker) Ready() bool {
	switch State(b.state.Load()) {
	case StateClosed:
		return true
	case StateOpen:
		return nowNanos()-b.openedAt.Load() >= int64(b.recovery)
	case StateHalfOpen:
		return nowNanos()-b.lastProbe.Load() >= int64(b.recovery)
	default:
		return true
	}
}

func (b *Breaker) Allow() bool {
	switch State(b.state.Load()) {
	case StateClosed:
		return true

	case StateOpen:
		if nowNanos()-b.openedAt.Load() < int64(b.recovery) {
			return false
		}
		b.state.CompareAndSwap(int32(StateOpen), int32(StateHalfOpen))
		fallthrough

	case StateHalfOpen:
		last := b.lastProbe.Load()
		now := nowNanos()
		if now-last < int64(b.recovery) {
			return false
		}
		return b.lastProbe.CompareAndSwap(last, now)

	default:
		return true
	}
}

func (b *Breaker) Observe(failed bool) {
	switch State(b.state.Load()) {
	case StateClosed:
		if !failed {
			return
		}
		if b.window.Add(time.Now()) >= b.threshold {
			if b.state.CompareAndSwap(int32(StateClosed), int32(StateOpen)) {
				b.openedAt.Store(nowNanos())
			}
		}

	case StateHalfOpen:
		if failed {
			if b.state.CompareAndSwap(int32(StateHalfOpen), int32(StateOpen)) {
				b.openedAt.Store(nowNanos())
			}
		} else {
			if b.state.CompareAndSwap(int32(StateHalfOpen), int32(StateClosed)) {
				b.window.Reset()
			}
		}
	case StateOpen:
	}
}

func nowNanos() int64 { return time.Now().UnixNano() }

func (b *Breaker) CurrentState() State {
	return State(b.state.Load())
}
