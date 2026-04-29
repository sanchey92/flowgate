package tcp

import "github.com/sanchey92/flowgate/internal/domain/model"

type Balancer interface {
	Pick() (*model.Backend, error)
	Release(backend *model.Backend)
}

type BufferPool interface {
	Get() *[]byte
	Put(*[]byte)
}
