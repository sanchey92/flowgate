package pool

import "sync"

type BufferPool struct {
	pool *sync.Pool
	size int
}

func NewBufferPool(size int) *BufferPool {
	return &BufferPool{
		size: size,
		pool: &sync.Pool{
			New: func() any {
				b := make([]byte, size)
				return &b
			},
		},
	}
}

func (p *BufferPool) Get() *[]byte {
	return p.pool.Get().(*[]byte)
}

func (p *BufferPool) Put(bp *[]byte) {
	*bp = (*bp)[:0]
	p.pool.Put(bp)
}
