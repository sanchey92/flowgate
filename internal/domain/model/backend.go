package model

import "sync/atomic"

type Backend struct {
	ID          string
	Addr        string
	Weight      int
	ActiveConns atomic.Int64
}
