package proxyhttp

type underlyingPool interface {
	Get() *[]byte
	Put(*[]byte)
}

type httpBufferPool struct {
	p underlyingPool
}

func newHTTPBufferPool(p underlyingPool) *httpBufferPool {
	return &httpBufferPool{p: p}
}

func (h *httpBufferPool) Get() []byte {
	bp := h.p.Get()
	return (*bp)[:cap(*bp)]
}

func (h *httpBufferPool) Put(b []byte) {
	h.p.Put(&b)
}
