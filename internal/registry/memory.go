package registry

import (
	"sync/atomic"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

type snap struct {
	backends []*model.Backend
	version  uint64
}

type InMemory struct {
	snap atomic.Pointer[snap]
}

func NewInMemory(backends []*model.Backend) *InMemory {
	p := &InMemory{}
	p.snap.Store(&snap{
		backends: backends,
		version:  1,
	})
	return p
}

func (p *InMemory) Snapshot() []*model.Backend {
	s := p.snap.Load()
	if s == nil || len(s.backends) == 0 {
		return nil
	}
	out := make([]*model.Backend, len(s.backends))
	copy(out, s.backends)
	return out
}

func (p *InMemory) Version() uint64 {
	s := p.snap.Load()
	if s == nil {
		return 0
	}
	return s.version
}

func (p *InMemory) Load() ([]*model.Backend, uint64) {
	s := p.snap.Load()
	if s == nil {
		return nil, 0
	}
	return s.backends, s.version
}

func (p *InMemory) Swap(newList []*model.Backend) []*model.Backend {
	for {
		old := p.snap.Load()
		var oldList []*model.Backend
		var oldVer uint64
		if old != nil {
			oldList = old.backends
			oldVer = old.version
		}
		newSwap := &snap{
			backends: newList,
			version:  oldVer + 1,
		}
		if p.snap.CompareAndSwap(old, newSwap) {
			return oldList
		}
	}
}
