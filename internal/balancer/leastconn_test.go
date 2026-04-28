package balancer_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sanchey92/flowgate/internal/balancer"
	"github.com/sanchey92/flowgate/internal/balancer/mocks"
	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
)

func TestNewLeastConn(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	lc := balancer.NewLeastConn(snap)
	require.NotNil(t, lc)
}

func TestLeastConn_Pick_NoBackends(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	snap.EXPECT().Snapshot().Return(nil).Once()

	lc := balancer.NewLeastConn(snap)
	got, err := lc.Pick()

	assert.Nil(t, got)
	assert.True(t, errors.Is(err, domainErr.ErrNoBackends))
}

func TestLeastConn_Pick_EmptySliceReturnsError(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	snap.EXPECT().Snapshot().Return([]*model.Backend{}).Once()

	lc := balancer.NewLeastConn(snap)
	got, err := lc.Pick()

	assert.Nil(t, got)
	assert.True(t, errors.Is(err, domainErr.ErrNoBackends))
}

func TestLeastConn_Pick_SingleBackend(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	snap.EXPECT().Snapshot().Return([]*model.Backend{a})

	lc := balancer.NewLeastConn(snap)

	for i := 0; i < 5; i++ {
		got, err := lc.Pick()
		require.NoError(t, err)
		assert.Same(t, a, got)
	}
	assert.Equal(t, int64(5), a.ActiveConns.Load())
}

func TestLeastConn_Pick_PicksBackendWithLeastConnections(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 1, 1)
	c := model.NewBackend("c:1", 1, 2)

	a.ActiveConns.Store(5)
	b.ActiveConns.Store(2)
	c.ActiveConns.Store(7)

	snap.EXPECT().Snapshot().Return([]*model.Backend{a, b, c}).Once()

	lc := balancer.NewLeastConn(snap)
	got, err := lc.Pick()

	require.NoError(t, err)
	assert.Same(t, b, got, "должен быть выбран backend с наименьшим числом соединений")
	assert.Equal(t, int64(3), b.ActiveConns.Load(), "ActiveConns должен инкрементиться после Pick")
}

func TestLeastConn_Pick_FirstBackendIsLeast(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 1, 1)
	c := model.NewBackend("c:1", 1, 2)

	a.ActiveConns.Store(1)
	b.ActiveConns.Store(5)
	c.ActiveConns.Store(3)

	snap.EXPECT().Snapshot().Return([]*model.Backend{a, b, c}).Once()

	lc := balancer.NewLeastConn(snap)
	got, err := lc.Pick()

	require.NoError(t, err)
	assert.Same(t, a, got)
	assert.Equal(t, int64(2), a.ActiveConns.Load())
}

func TestLeastConn_Pick_TiebreakerHigherWeightWins(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 5, 1)
	c := model.NewBackend("c:1", 3, 2)

	// Все с одинаковым числом соединений → должен победить b с максимальным весом.
	snap.EXPECT().Snapshot().Return([]*model.Backend{a, b, c}).Once()

	lc := balancer.NewLeastConn(snap)
	got, err := lc.Pick()

	require.NoError(t, err)
	assert.Same(t, b, got, "при равных соединениях должен побеждать backend с максимальным весом")
}

func TestLeastConn_Pick_TiebreakerEqualWeightFirstWins(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 2, 0)
	b := model.NewBackend("b:1", 2, 1)
	c := model.NewBackend("c:1", 2, 2)

	// Полное равенство (conns и weight) → первый в списке.
	snap.EXPECT().Snapshot().Return([]*model.Backend{a, b, c}).Once()

	lc := balancer.NewLeastConn(snap)
	got, err := lc.Pick()

	require.NoError(t, err)
	assert.Same(t, a, got, "при полном равенстве должен быть выбран первый backend")
}

func TestLeastConn_Pick_LeastConnsBeatsHigherWeight(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 10, 0)
	b := model.NewBackend("b:1", 1, 1)

	a.ActiveConns.Store(5)
	b.ActiveConns.Store(1)

	// Меньше соединений побеждает любой вес.
	snap.EXPECT().Snapshot().Return([]*model.Backend{a, b}).Once()

	lc := balancer.NewLeastConn(snap)
	got, err := lc.Pick()

	require.NoError(t, err)
	assert.Same(t, b, got)
}

func TestLeastConn_Pick_DistributesAcrossEqualBackends(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 1, 1)
	c := model.NewBackend("c:1", 1, 2)

	snap.EXPECT().Snapshot().Return([]*model.Backend{a, b, c})

	lc := balancer.NewLeastConn(snap)

	const picks = 9
	for i := 0; i < picks; i++ {
		_, err := lc.Pick()
		require.NoError(t, err)
	}

	// Так как Pick инкрементит ActiveConns, на следующей итерации выбирается тот, у кого меньше.
	// При равных весах должно получиться равномерное распределение.
	assert.Equal(t, int64(3), a.ActiveConns.Load())
	assert.Equal(t, int64(3), b.ActiveConns.Load())
	assert.Equal(t, int64(3), c.ActiveConns.Load())
}

func TestLeastConn_Realise_NilBackend(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	lc := balancer.NewLeastConn(snap)

	assert.NotPanics(t, func() {
		lc.Release(nil)
	})
}

func TestLeastConn_Realise_DecrementsActiveConns(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	lc := balancer.NewLeastConn(snap)

	b := model.NewBackend("a:1", 1, 0)
	b.ActiveConns.Store(3)

	lc.Release(b)
	assert.Equal(t, int64(2), b.ActiveConns.Load())

	lc.Release(b)
	lc.Release(b)
	assert.Equal(t, int64(0), b.ActiveConns.Load())
}

func TestLeastConn_Realise_NeverGoesBelowZero(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	lc := balancer.NewLeastConn(snap)

	b := model.NewBackend("a:1", 1, 0)
	// Изначально 0 — Realise не должен сделать значение отрицательным.
	for i := 0; i < 10; i++ {
		lc.Release(b)
	}
	assert.Equal(t, int64(0), b.ActiveConns.Load())
}

func TestLeastConn_PickThenRealise_ReturnsCounterToZero(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	snap.EXPECT().Snapshot().Return([]*model.Backend{a})

	lc := balancer.NewLeastConn(snap)

	const n = 5
	picked := make([]*model.Backend, 0, n)
	for i := 0; i < n; i++ {
		got, err := lc.Pick()
		require.NoError(t, err)
		picked = append(picked, got)
	}
	assert.Equal(t, int64(n), a.ActiveConns.Load())

	for _, p := range picked {
		lc.Release(p)
	}
	assert.Equal(t, int64(0), a.ActiveConns.Load())
}

func TestLeastConn_Pick_Concurrent(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 1, 1)
	c := model.NewBackend("c:1", 1, 2)
	backends := []*model.Backend{a, b, c}
	snap.EXPECT().Snapshot().Return(backends)

	lc := balancer.NewLeastConn(snap)

	const (
		workers      = 16
		picksPerGoro = 1000
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < picksPerGoro; i++ {
				got, err := lc.Pick()
				assert.NoError(t, err)
				require.NotNil(t, got)
				lc.Release(got)
			}
		}()
	}
	wg.Wait()

	for _, be := range backends {
		assert.Equal(t, int64(0), be.ActiveConns.Load(),
			"ActiveConns у %s должен быть 0 после Pick+Realise", be.ID)
	}
}

func TestLeastConn_PickIncrement_Concurrent(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	a := model.NewBackend("a:1", 1, 0)
	b := model.NewBackend("b:1", 1, 1)
	backends := []*model.Backend{a, b}
	snap.EXPECT().Snapshot().Return(backends)

	lc := balancer.NewLeastConn(snap)

	const (
		workers      = 8
		picksPerGoro = 500
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < picksPerGoro; i++ {
				_, err := lc.Pick()
				assert.NoError(t, err)
			}
		}()
	}
	wg.Wait()

	total := a.ActiveConns.Load() + b.ActiveConns.Load()
	assert.Equal(t, int64(workers*picksPerGoro), total, "ни один инкремент не должен быть потерян")
}

func TestLeastConn_Realise_Concurrent(t *testing.T) {
	snap := mocks.NewSnapshotter(t)
	lc := balancer.NewLeastConn(snap)

	b := model.NewBackend("a:1", 1, 0)
	const initial = int64(1000)
	b.ActiveConns.Store(initial)

	const workers = 16
	perGoro := int(initial) / workers // 16 * 62 = 992 декрементов

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoro; i++ {
				lc.Release(b)
			}
		}()
	}
	wg.Wait()

	expected := initial - int64(workers*perGoro)
	assert.Equal(t, expected, b.ActiveConns.Load(), "Realise должен корректно декрементить конкурентно")

	// Дополнительный «оверкилл» Realise не должен уводить значение в минус.
	var wg2 sync.WaitGroup
	wg2.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg2.Done()
			for i := 0; i < 1000; i++ {
				lc.Release(b)
			}
		}()
	}
	wg2.Wait()

	got := b.ActiveConns.Load()
	assert.GreaterOrEqual(t, got, int64(0), "счётчик не должен уходить в минус")
	assert.Equal(t, int64(0), got)
}

func TestLeastConn_Realise_DoesNotConsumeFreshPick(t *testing.T) {
	b := model.NewBackend("a:1", 1, 0)
	snap := mocks.NewSnapshotter(t)
	snap.EXPECT().Snapshot().Return([]*model.Backend{b})
	lc := balancer.NewLeastConn(snap)

	lc.Release(b)
	lc.Release(b)
	require.Equal(t, int64(0), b.ActiveConns.Load())

	got, err := lc.Pick()
	require.NoError(t, err)
	assert.Same(t, b, got)
	assert.Equal(t, int64(1), b.ActiveConns.Load())

	lc.Release(b)
	assert.Equal(t, int64(0), b.ActiveConns.Load())
}

func TestLeastConn_PickRealise_Race_SingleBackend(t *testing.T) {
	a := model.NewBackend("a:1", 1, 0)
	snap := mocks.NewSnapshotter(t)
	snap.EXPECT().Snapshot().Return([]*model.Backend{a})
	lc := balancer.NewLeastConn(snap)

	const workers = 32
	const opsPerGoro = 500

	var picked atomic.Int64

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoro; i++ {
				got, err := lc.Pick()
				assert.NoError(t, err)
				picked.Add(1)
				lc.Release(got)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(workers*opsPerGoro), picked.Load())
	assert.Equal(t, int64(0), a.ActiveConns.Load())
}
