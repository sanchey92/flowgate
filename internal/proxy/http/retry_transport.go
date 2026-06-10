package proxyhttp

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
	"github.com/sanchey92/flowgate/internal/domain/model"
	"github.com/sanchey92/flowgate/internal/proxy/http/reqctx"
	"github.com/sanchey92/flowgate/internal/proxy/http/retry"
)

const maxDrainBytes = 16 << 10

type retryTransport struct {
	inner  http.RoundTripper
	groups map[string]Balancer
	policy *retry.Policy
	log    *slog.Logger
}

func newRetryTransport(
	inner http.RoundTripper,
	groups map[string]Balancer,
	policy *retry.Policy,
	log *slog.Logger,
) *retryTransport {
	return &retryTransport{
		inner:  inner,
		groups: groups,
		policy: policy,
		log:    log,
	}
}

func (t *retryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	slot := reqctx.SlotFrom(r.Context())
	if slot == nil {
		return nil, errors.New("http proxy: missing request slot in context")
	}

	if slot.PickErr != nil {
		return nil, fmt.Errorf("http proxy pick: %w", slot.PickErr)
	}

	bal := t.groups[slot.Group]
	if bal == nil {
		return nil, fmt.Errorf("http proxy %w: %q", domainErr.ErrUnknownGroup, slot.Group)
	}

	maxAttempts, err := t.maxAttempts(r, slot)
	if err != nil {
		return nil, fmt.Errorf("http proxy: %w", err)
	}
	return t.roundTrip(r, slot, bal, maxAttempts)
}

func (t *retryTransport) maxAttempts(r *http.Request, slot *reqctx.RequestSlot) (int, error) {
	if !t.policy.Enabled() || slot.Upgrade != "" || !t.policy.MethodAllowsRetry(r) {
		return 1, nil
	}
	replayable, err := retry.EnsureReplayableBody(r)
	if err != nil {
		return 0, fmt.Errorf("http proxy: prepare body: %w", err)
	}
	if !replayable {
		return 1, nil
	}
	return 1 + t.policy.MaxRetries(), nil
}

func (t *retryTransport) roundTrip(
	r *http.Request,
	slot *reqctx.RequestSlot,
	bal Balancer,
	maxAttempts int,
) (*http.Response, error) {
	var (
		lastErr error
		prev    *model.Backend
	)
	defer func() {
		if prev != nil {
			bal.Release(prev)
		}
	}()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := t.prepareAttempt(r, attempt); err != nil {
			return nil, orErr(lastErr, err)
		}

		backend, err := bal.Pick()
		if err != nil {
			return nil, orErr(lastErr, fmt.Errorf("http proxy pick: %w", err))
		}
		if backend == nil {
			return nil, orErr(lastErr, fmt.Errorf("http proxy pick: %w", domainErr.ErrNilBackend))
		}

		if prev != nil {
			bal.Release(prev)
			prev = nil
		}
		slot.Backend = backend
		directTo(r, backend)

		resp, rtErr := t.inner.RoundTrip(r)
		failed := isUpstreamFailure(resp, rtErr)
		backend.Observe(failed)

		last := attempt == maxAttempts-1
		if !failed || last || !t.policy.RetryableOutcome(resp, rtErr) {
			if rtErr != nil {
				return nil, fmt.Errorf("http proxy: round trip: %w", rtErr)
			}
			return resp, nil
		}
		drainAndClose(resp)
		prev = backend
		slot.Backend = nil
		slot.Retries++
		lastErr = rtErr
	}
	return nil, orErr(lastErr, fmt.Errorf("http proxy: %w", domainErr.ErrAllBackendsUnhealthy))
}

// orErr возвращает primary, если он не nil, иначе fallback.
// Локальная замена cmp.Or: wrapcheck требует оборачивать ошибки,
// возвращённые из внешних пакетов.
func orErr(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}

func (t *retryTransport) prepareAttempt(req *http.Request, attempt int) error {
	if err := req.Context().Err(); err != nil {
		return fmt.Errorf("http proxy: %w", err)
	}
	if attempt == 0 {
		return nil
	}
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return fmt.Errorf("http proxy: rewind body: %w", err)
		}
		req.Body = body
	}
	if err := retry.Sleep(req.Context(), t.policy.BackoffDelay(attempt-1)); err != nil {
		return fmt.Errorf("http proxy: backoff: %w", err)
	}
	return nil
}

func directTo(req *http.Request, b *model.Backend) {
	req.URL.Scheme = "http"
	req.URL.Host = b.Addr
	req.Host = ""
}

func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	_ = resp.Body.Close()
}
