package balancer

import (
	"sync"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
)

type Sourcer interface {
	Load() ([]*model.Backend, uint64)
}

type RoundRobin struct {
	src Sourcer
	mu  sync.Mutex
	cw  []int
	ver uint64
}

func NewRoundRobin(src Sourcer) *RoundRobin {
	rr := &RoundRobin{src: src}
	rr.resync()
	return rr
}

func (rr *RoundRobin) resync() {
	backends, ver := rr.src.Load()
	rr.ver = ver
	rr.cw = make([]int, len(backends))
}

func (rr *RoundRobin) Pick() (*model.Backend, error) {
	rr.mu.Lock()
	defer rr.mu.Unlock()

	backends, ver := rr.src.Load()
	if ver != rr.ver {
		rr.ver = ver
		rr.cw = make([]int, len(backends))
	}
	if len(backends) == 0 {
		return nil, domainErr.ErrNoBackends
	}

	total := 0
	bestIdx := -1
	for i, b := range backends {
		rr.cw[i] += b.Weight
		total += b.Weight
		if bestIdx == -1 || rr.cw[i] > rr.cw[bestIdx] {
			bestIdx = i
		}
	}
	rr.cw[bestIdx] -= total
	return backends[bestIdx], nil
}
