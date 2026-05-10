package balancer

import (
	"fmt"
	"strings"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

type Balancer interface {
	Pick() (*model.Backend, error)
	Release(*model.Backend)
}

type BackendSource interface {
	Sourcer
	Snapshotter
}

type Kind string

const (
	KindRoundRobin Kind = "round_robin"
	KindLeastConn  Kind = "least_conn"
)

func New(kind string, src BackendSource) (Balancer, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(kind))) {
	case KindRoundRobin:
		return NewRoundRobin(src), nil
	case KindLeastConn:
		return NewLeastConn(src), nil
	default:
		return nil, fmt.Errorf("balancer: unknown kind %q", kind)
	}
}
