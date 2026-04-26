package balancer_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/sanchey92/flowgate/internal/balancer"
	"github.com/sanchey92/flowgate/internal/balancer/mocks"
	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNewRoundRobin(t *testing.T) {
	src := mocks.NewSourcer(t)
	src.EXPECT().
		Load().
		Return([]*model.Backend{model.NewBackend("a:1", 1, 0)}, uint64(1)).
		Once()

	rr := balancer.NewRoundRobin(src)
	require.NotNil(t, rr)
}

func TestRoundRobin_Pick_NoBackends(t *testing.T) {
	src := mocks.NewSourcer(t)
	src.EXPECT().Load().Return(nil, uint64(1))

	rr := balancer.NewRoundRobin(src)
	got, err := rr.Pick()

	assert.Nil(t, got)
	assert.True(t, errors.Is(err, domainErr.ErrNoBackends))
}

func TestRoundRobin_Pick_SingleBackend(t *testing.T) {
	src := mocks.NewSourcer(t)
	backend := model.NewBackend("a:1", 1, 0)
	src.EXPECT().Load().Return([]*model.Backend{backend}, uint64(1))

	rr := balancer.NewRoundRobin(src)

	for i := 0; i < 5; i++ {
		got, err := rr.Pick()
		require.NoError(t, err)
		assert.Same(t, backend, got)
	}
}

func TestRoundRobin_Pick_EqualWeightsDegradesToRoundRobin(t *testing.T) {
	src := mocks.NewSourcer(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 1, 1)
	c := model.NewBackend("c:1", 1, 2)
	src.EXPECT().Load().Return([]*model.Backend{a, b, c}, uint64(1))

	rr := balancer.NewRoundRobin(src)

	// При равных весах SWRR вырождается в строгий round-robin: a, b, c, a, b, c, ...
	expected := []*model.Backend{a, b, c, a, b, c, a, b, c}
	for i, want := range expected {
		got, err := rr.Pick()
		require.NoError(t, err, "iteration %d", i)
		assert.Samef(t, want, got, "iteration %d: want %s, got %s", i, want.ID, got.ID)
	}
}

func TestRoundRobin_Pick_SWRRSequence(t *testing.T) {
	src := mocks.NewSourcer(t)
	a := model.NewBackend("a:1", 5, 0)
	b := model.NewBackend("b:1", 1, 1)
	c := model.NewBackend("c:1", 1, 2)
	src.EXPECT().Load().Return([]*model.Backend{a, b, c}, uint64(1))

	rr := balancer.NewRoundRobin(src)

	expected := []*model.Backend{a, a, b, a, c, a, a}
	for i, want := range expected {
		got, err := rr.Pick()
		require.NoError(t, err, "iteration %d", i)
		assert.Samef(t, want, got, "iteration %d: want %s, got %s", i, want.ID, got.ID)
	}
}

func TestRoundRobin_Pick_WeightedDistribution(t *testing.T) {
	src := mocks.NewSourcer(t)
	a := model.NewBackend("a:1", 5, 0)
	b := model.NewBackend("b:1", 1, 1)
	c := model.NewBackend("c:1", 1, 2)
	src.EXPECT().Load().Return([]*model.Backend{a, b, c}, uint64(1))

	rr := balancer.NewRoundRobin(src)

	const (
		cycles   = 100
		cycleLen = 7 // 5 + 1 + 1
	)

	counts := map[string]int{}
	for i := 0; i < cycles*cycleLen; i++ {
		got, err := rr.Pick()
		require.NoError(t, err)
		counts[got.ID]++
	}

	assert.Equal(t, 5*cycles, counts[a.ID], "backend a should get share = weight")
	assert.Equal(t, 1*cycles, counts[b.ID], "backend b should get share = weight")
	assert.Equal(t, 1*cycles, counts[c.ID], "backend c should get share = weight")
}

func TestRoundRobin_Pick_ResyncOnVersionChange(t *testing.T) {
	src := mocks.NewSourcer(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 1, 1)

	type srcState struct {
		backends []*model.Backend
		version  uint64
	}
	cur := atomic.Pointer[srcState]{}
	cur.Store(&srcState{backends: []*model.Backend{a}, version: 1})

	src.EXPECT().Load().RunAndReturn(func() ([]*model.Backend, uint64) {
		s := cur.Load()
		return s.backends, s.version
	})

	rr := balancer.NewRoundRobin(src)

	got, err := rr.Pick()
	require.NoError(t, err)
	assert.Same(t, a, got)

	// Источник заменился: теперь [a, b], версия 2 — RR должен сбросить cw.
	cur.Store(&srcState{backends: []*model.Backend{a, b}, version: 2})

	// После сброса cw=[0,0] первый Pick идёт первому (a), второй — второму (b).
	got, err = rr.Pick()
	require.NoError(t, err)
	assert.Same(t, a, got, "first pick after resync must hit first backend")

	got, err = rr.Pick()
	require.NoError(t, err)
	assert.Same(t, b, got, "second pick after resync must hit second backend")
}

func TestRoundRobin_Pick_ResyncToEmptyReturnsError(t *testing.T) {
	src := mocks.NewSourcer(t)
	a := model.NewBackend("a:1", 1, 0)

	type srcState struct {
		backends []*model.Backend
		version  uint64
	}
	cur := atomic.Pointer[srcState]{}
	cur.Store(&srcState{backends: []*model.Backend{a}, version: 1})

	src.EXPECT().Load().RunAndReturn(func() ([]*model.Backend, uint64) {
		s := cur.Load()
		return s.backends, s.version
	})

	rr := balancer.NewRoundRobin(src)

	got, err := rr.Pick()
	require.NoError(t, err)
	assert.Same(t, a, got)

	cur.Store(&srcState{backends: nil, version: 2})

	got, err = rr.Pick()
	assert.Nil(t, got)
	assert.True(t, errors.Is(err, domainErr.ErrNoBackends))
}

func TestRoundRobin_Pick_Concurrent(t *testing.T) {
	src := mocks.NewSourcer(t)
	a := model.NewBackend("a:1", 2, 0)
	b := model.NewBackend("b:1", 3, 1)
	c := model.NewBackend("c:1", 5, 2)
	backends := []*model.Backend{a, b, c}
	src.EXPECT().Load().Return(backends, uint64(1))

	rr := balancer.NewRoundRobin(src)

	const (
		workers      = 16
		picksPerGoro = 1000
		totalWeight  = 2 + 3 + 5
	)

	counts := sync.Map{}
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < picksPerGoro; i++ {
				got, err := rr.Pick()
				assert.NoError(t, err)
				assert.NotNil(t, got)
				v, _ := counts.LoadOrStore(got.ID, new(int64))
				atomic.AddInt64(v.(*int64), 1)
			}
		}()
	}
	wg.Wait()

	var total int64
	counts.Range(func(_, v any) bool {
		total += atomic.LoadInt64(v.(*int64))
		return true
	})
	assert.Equal(t, int64(workers*picksPerGoro), total, "no picks should have been lost")

	// Доли должны соответствовать весам с допуском (общая нагрузка кратна циклу).
	if (workers*picksPerGoro)%totalWeight == 0 {
		cycles := int64(workers*picksPerGoro) / totalWeight
		for _, be := range backends {
			v, ok := counts.Load(be.ID)
			require.True(t, ok, "backend %s was never picked", be.ID)
			assert.Equal(t, int64(be.Weight)*cycles, atomic.LoadInt64(v.(*int64)),
				"backend %s share must equal weight*cycles", be.ID)
		}
	}
}
