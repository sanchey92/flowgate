package passive

import (
	"sync"
	"time"
)

type windowCounter struct {
	mu          sync.Mutex
	buckets     []int64
	epochs      []int64
	bucketWidth time.Duration
}

func newWindowCounter(window time.Duration, n int) *windowCounter {
	if n <= 0 {
		n = windowBuckets
	}
	return &windowCounter{
		buckets:     make([]int64, n),
		epochs:      make([]int64, n),
		bucketWidth: window / time.Duration(n),
	}
}

func (w *windowCounter) Add(now time.Time) int64 {
	epoch := now.UnixNano() / int64(w.bucketWidth)
	idx := int(epoch % int64(len(w.buckets)))
	if idx < 0 {
		idx += len(w.buckets)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.epochs[idx] != epoch {
		w.buckets[idx] = 0
		w.epochs[idx] = epoch
	}
	w.buckets[idx]++

	return w.sumLocked(epoch)
}

func (w *windowCounter) sumLocked(epoch int64) int64 {
	minEpoch := epoch - int64(len(w.buckets)) + 1
	var sum int64
	for i := range w.buckets {
		if w.epochs[i] >= minEpoch {
			sum += w.buckets[i]
		}
	}
	return sum
}

func (w *windowCounter) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.buckets {
		w.buckets[i] = 0
		w.epochs[i] = 0
	}
}
