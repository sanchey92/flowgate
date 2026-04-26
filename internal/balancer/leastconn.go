package balancer

import (
	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
)

type Snapshotter interface {
	Snapshot() []*model.Backend
}

type LeastConn struct {
	snap Snapshotter
}

func NewLeastConn(snap Snapshotter) *LeastConn {
	return &LeastConn{snap: snap}
}

func (lc *LeastConn) Pick() (*model.Backend, error) {
	backends := lc.snap.Snapshot()
	if len(backends) == 0 {
		return nil, domainErr.ErrNoBackends
	}

	best := backends[0]
	bestConns := best.ActiveConns.Load()
	for _, b := range backends[1:] {
		c := b.ActiveConns.Load()
		switch {
		case c < bestConns:
			best, bestConns = b, c
		case c == bestConns && b.Weight > best.Weight:
			best = b
		}
	}

	best.ActiveConns.Add(1)
	return best, nil
}

func (lc *LeastConn) Realise(b *model.Backend) {
	if b == nil {
		return
	}

	for {
		cur := b.ActiveConns.Load()
		if cur <= 0 {
			return
		}
		if b.ActiveConns.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}
