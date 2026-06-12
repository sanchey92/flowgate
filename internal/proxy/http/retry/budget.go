package retry

import (
	"sync"
	"time"
)

const (
	budgetWindow     = 10 * time.Second
	budgetBuckets    = 10
	budgetMinRequest = 10
)

type Budget struct {
	percent int64

	mu      sync.Mutex
	buckets []budgetBucket
	width   time.Duration
}

type budgetBucket struct {
	epoch   int64
	total   int64
	retried int64
}

func NewBudget(percent int) *Budget {
	return &Budget{
		percent: int64(percent),
		buckets: make([]budgetBucket, budgetBuckets),
		width:   budgetWindow / budgetBuckets,
	}
}

func (b *Budget) RecordRequest() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bucketLocked(time.Now()).total++
}

func (b *Budget) AllowRetry() bool {
	now := time.Now()

	b.mu.Lock()
	defer b.mu.Unlock()

	total, retried := b.sumLocked(now)
	if total >= budgetMinRequest && (retried+1)*100 > b.percent*(total+1) {
		return false
	}
	b.bucketLocked(now).retried++
	return true
}

func (b *Budget) bucketLocked(now time.Time) *budgetBucket {
	epoch := now.UnixNano() / int64(b.width)
	idx := int(epoch % int64(len(b.buckets)))
	if idx < 0 {
		idx += len(b.buckets)
	}
	bkt := &b.buckets[idx]
	if bkt.epoch != epoch {
		*bkt = budgetBucket{epoch: epoch}
	}
	return bkt
}

func (b *Budget) sumLocked(now time.Time) (total, retried int64) {
	minEpoch := now.UnixNano()/int64(b.width) - int64(len(b.buckets)) + 1
	for i := range b.buckets {
		if b.buckets[i].epoch >= minEpoch {
			total += b.buckets[i].total
			retried += b.buckets[i].retried
		}
	}
	return total, retried
}
