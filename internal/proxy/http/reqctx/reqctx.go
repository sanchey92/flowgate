package reqctx

import (
	"context"
	"time"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

type ctxKey int

const ctxKeySlot ctxKey = 0

type RequestSlot struct {
	RequestID  string
	Backend    *model.Backend
	Group      string
	PickErr    error
	StartedAt  time.Time
	ClientIP   string
	ClientAddr string
	Host       string
	Method     string
	Path       string
	Scheme     string
}

func SlotFrom(ctx context.Context) *RequestSlot {
	s, _ := ctx.Value(ctxKeySlot).(*RequestSlot)
	return s
}

func WithSlot(ctx context.Context, s *RequestSlot) context.Context {
	return context.WithValue(ctx, ctxKeySlot, s)
}
