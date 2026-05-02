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

func (c *Concurrency) Acquire(ctx context.Context) error {
	if c.sem == nil {
		return nil
	}
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Concurrency) Release() {
	if c.sem != nil {
		<-c.sem
	}
}
