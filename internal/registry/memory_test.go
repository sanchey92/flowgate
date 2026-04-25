package registry_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/registry"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNewInMemory(t *testing.T) {
	tests := []struct {
		name string
		in   []*model.Backend
	}{
		{
			name: "nil input produces empty pool",
			in:   nil,
		},
		{
			name: "single backend stored as-is",
			in: []*model.Backend{
				model.NewBackend("10.0.0.1:80", 5, 0),
			},
		},
		{
			name: "multiple backends preserve order",
			in: []*model.Backend{
				model.NewBackend("a:1", 2, 0),
				model.NewBackend("b:2", 3, 1),
				model.NewBackend("a:1", 7, 2),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := registry.NewInMemory(tt.in)
			require.NotNil(t, r)

			assert.EqualValues(t, 1, r.Version(), "fresh pool starts at version 1")

			snap := r.Snapshot()
			if len(tt.in) == 0 {
				assert.Empty(t, snap)
				return
			}

			require.Len(t, snap, len(tt.in))
			for i, want := range tt.in {
				assert.Same(t, want, snap[i],
					"snapshot must reference the same Backend instances (shared ActiveConns)")
			}
		})
	}
}

func TestInMemory_Snapshot_ReturnsCopy(t *testing.T) {
	r := registry.NewInMemory([]*model.Backend{
		model.NewBackend("a:1", 1, 0),
		model.NewBackend("b:2", 2, 1),
	})

	first := r.Snapshot()
	require.Len(t, first, 2)

	first[0] = nil

	second := r.Snapshot()
	require.Len(t, second, 2)
	assert.NotNil(t, second[0], "mutating returned slice must not affect pool")
	assert.Equal(t, "a:1#0", second[0].ID)
}

func TestInMemory_Snapshot_EmptyPoolReturnsNil(t *testing.T) {
	r := registry.NewInMemory(nil)
	assert.Nil(t, r.Snapshot())
}

func TestInMemory_Load(t *testing.T) {
	backends := []*model.Backend{
		model.NewBackend("a:1", 3, 0),
		model.NewBackend("b:2", 4, 1),
	}
	r := registry.NewInMemory(backends)

	list, ver := r.Load()
	require.Len(t, list, 2)
	assert.EqualValues(t, 1, ver)
	assert.Equal(t, "a:1#0", list[0].ID)
	assert.Equal(t, "b:2#1", list[1].ID)
}

func TestInMemory_Version(t *testing.T) {
	r := registry.NewInMemory(nil)
	assert.EqualValues(t, 1, r.Version())

	r.Swap([]*model.Backend{model.NewBackend("x:1", 1, 0)})
	assert.EqualValues(t, 2, r.Version())

	r.Swap(nil)
	assert.EqualValues(t, 3, r.Version(), "Swap to nil still bumps version")
}

func TestInMemory_Swap_ReplacesAndReturnsOld(t *testing.T) {
	r := registry.NewInMemory([]*model.Backend{
		model.NewBackend("old:1", 1, 0),
	})

	newList := []*model.Backend{
		model.NewBackend("new:1", 2, 0),
		model.NewBackend("new:2", 3, 1),
	}

	old := r.Swap(newList)

	require.Len(t, old, 1)
	assert.Equal(t, "old:1#0", old[0].ID)

	snap := r.Snapshot()
	require.Len(t, snap, 2)
	assert.Equal(t, "new:1#0", snap[0].ID)
	assert.Equal(t, "new:2#1", snap[1].ID)
	assert.EqualValues(t, 2, r.Version())
}

func TestInMemory_Swap_EmptyToNonEmpty(t *testing.T) {
	r := registry.NewInMemory(nil)

	old := r.Swap([]*model.Backend{model.NewBackend("n:1", 1, 0)})
	assert.Empty(t, old)

	list, ver := r.Load()
	require.Len(t, list, 1)
	assert.Equal(t, "n:1#0", list[0].ID)
	assert.EqualValues(t, 2, ver)
}

func TestInMemory_Swap_Concurrent(t *testing.T) {
	const (
		writers = 16
		swaps   = 200
	)

	r := registry.NewInMemory([]*model.Backend{model.NewBackend("init:1", 1, 0)})
	startVer := r.Version()

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := range writers {
		go func() {
			defer wg.Done()
			for i := range swaps {
				r.Swap([]*model.Backend{
					model.NewBackend(fmt.Sprintf("w%d", w), 1, i),
				})
			}
		}()
	}

	readerStop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var prev uint64
		for {
			select {
			case <-readerStop:
				return
			default:
			}
			_, v := r.Load()
			assert.GreaterOrEqual(t, v, prev, "version must be monotonic")
			prev = v
		}
	}()

	wg.Wait()
	close(readerStop)
	<-readerDone

	finalVer := r.Version()
	assert.EqualValues(t, startVer+writers*swaps, finalVer,
		"every Swap must increment the version exactly once (no lost CAS)")

	snap := r.Snapshot()
	require.Len(t, snap, 1)
}
