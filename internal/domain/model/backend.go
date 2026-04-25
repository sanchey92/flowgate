package model

import (
	"fmt"
	"sync/atomic"
)

type Backend struct {
	ID          string
	Addr        string
	Weight      int
	ActiveConns atomic.Int64
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
