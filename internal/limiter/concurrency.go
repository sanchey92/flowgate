package limiter

import "context"

type Concurrency struct {
	sem chan struct{}
}

func NewConcurrency(limit int) *Concurrency {
	if limit <= 0 {
		return &Concurrency{}
	}
	return &Concurrency{sem: make(chan struct{}, limit)}
}

func (c *Concurrency) Acquire(ctx context.Context) bool {
	if c.sem == nil {
		return true
	}
	select {
	case c.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Concurrency) Release() {
	if c.sem != nil {
		<-c.sem
	}
}
