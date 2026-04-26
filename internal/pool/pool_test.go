package pool_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/sanchey92/flowgate/internal/pool"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNewBufferPool(t *testing.T) {
	p := pool.NewBufferPool(1024)
	require.NotNil(t, p)
}

func TestBufferPool_Get_ReturnsBufferOfConfiguredSize(t *testing.T) {
	const size = 512
	p := pool.NewBufferPool(size)

	bp := p.Get()
	require.NotNil(t, bp)
	require.NotNil(t, *bp)
	assert.Equal(t, size, cap(*bp), "capacity must equal configured size")
	assert.Equal(t, size, len(*bp), "fresh buffer length equals capacity")
}

func TestBufferPool_Put_ResetsLengthToZero(t *testing.T) {
	const size = 256
	p := pool.NewBufferPool(size)

	bp := p.Get()
	require.NotNil(t, bp)
	require.Equal(t, size, len(*bp))

	p.Put(bp)
	assert.Equal(t, 0, len(*bp), "Put must reset length to 0")
	assert.Equal(t, size, cap(*bp), "Put must preserve capacity")
}

func TestBufferPool_GetAfterPut_PreservesCapacity(t *testing.T) {
	const size = 128
	p := pool.NewBufferPool(size)

	first := p.Get()
	p.Put(first)

	// sync.Pool не гарантирует переиспользование, но любой возвращённый буфер
	// обязан иметь корректную ёмкость.
	for i := 0; i < 10; i++ {
		bp := p.Get()
		require.NotNil(t, bp)
		assert.Equal(t, size, cap(*bp), "iteration %d", i)
		p.Put(bp)
	}
}

func TestBufferPool_Get_ReturnsUsableBuffer(t *testing.T) {
	const size = 64
	p := pool.NewBufferPool(size)

	bp := p.Get()
	defer p.Put(bp)

	buf := *bp
	for i := 0; i < size; i++ {
		buf[i] = byte(i)
	}
	for i := 0; i < size; i++ {
		assert.Equal(t, byte(i), buf[i])
	}
}

func TestBufferPool_PutThenAppend_RespectsCapacity(t *testing.T) {
	const size = 32
	p := pool.NewBufferPool(size)

	bp := p.Get()
	p.Put(bp)
	require.Equal(t, 0, len(*bp))

	*bp = append(*bp, []byte("hello")...)
	assert.Equal(t, 5, len(*bp))
	assert.Equal(t, size, cap(*bp), "append within capacity must not reallocate")
	assert.Equal(t, "hello", string(*bp))
}

func TestBufferPool_ZeroSize(t *testing.T) {
	p := pool.NewBufferPool(0)

	bp := p.Get()
	require.NotNil(t, bp)
	assert.Equal(t, 0, cap(*bp))
	assert.Equal(t, 0, len(*bp))

	p.Put(bp)
	assert.Equal(t, 0, len(*bp))
}

func TestBufferPool_Concurrent_GetPut(t *testing.T) {
	const (
		size       = 1024
		workers    = 32
		iterations = 1000
	)
	p := pool.NewBufferPool(size)

	var wg sync.WaitGroup
	var ops int64

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				bp := p.Get()
				require.NotNil(t, bp)
				assert.Equal(t, size, cap(*bp))

				buf := (*bp)[:size]
				buf[0] = 0xAA
				buf[size-1] = 0xBB

				p.Put(bp)
				atomic.AddInt64(&ops, 1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(workers*iterations), atomic.LoadInt64(&ops))
}

func TestBufferPool_Independent_Pools(t *testing.T) {
	p1 := pool.NewBufferPool(64)
	p2 := pool.NewBufferPool(256)

	b1 := p1.Get()
	b2 := p2.Get()

	assert.Equal(t, 64, cap(*b1))
	assert.Equal(t, 256, cap(*b2))

	p1.Put(b1)
	p2.Put(b2)
}
