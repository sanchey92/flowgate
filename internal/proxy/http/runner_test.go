package proxyhttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

func newRunnerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newRunnerForTest(handler http.Handler) *Runner {
	return NewRunner("test", "127.0.0.1:0", handler, RunnerSettings{
		ReadHeaderTimeout: 1 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       1 * time.Second,
	}, newRunnerTestLogger())
}

func TestRunner_StartShutdown_HappyPath(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))

	require.NoError(t, r.Start(context.Background()))
	require.NotNil(t, r.Addr())

	resp, err := http.Get("http://" + r.Addr().String() + "/")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, r.Shutdown(context.Background()))
}

func TestRunner_StartTwice_ReturnsAlreadyStarted(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.NotFoundHandler())
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	require.NoError(t, r.Start(context.Background()))
	err := r.Start(context.Background())
	assert.ErrorIs(t, err, domainErr.ErrProxyStarted)
}

func TestRunner_StartAfterShutdown_ReturnsAlreadyStopped(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.NotFoundHandler())
	require.NoError(t, r.Start(context.Background()))
	require.NoError(t, r.Shutdown(context.Background()))

	err := r.Start(context.Background())
	assert.ErrorIs(t, err, domainErr.ErrProxyStopped)
}

func TestRunner_ShutdownBeforeStart_NoError(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.NotFoundHandler())
	require.NoError(t, r.Shutdown(context.Background()))
}

func TestRunner_ShutdownIdempotent(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.NotFoundHandler())
	require.NoError(t, r.Start(context.Background()))

	require.NoError(t, r.Shutdown(context.Background()))
	require.NoError(t, r.Shutdown(context.Background()))
	require.NoError(t, r.Shutdown(context.Background()))
}

func TestRunner_ShutdownConcurrent(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.NotFoundHandler())
	require.NoError(t, r.Start(context.Background()))

	const callers = 8
	errs := make(chan error, callers)
	for range callers {
		go func() { errs <- r.Shutdown(context.Background()) }()
	}
	for range callers {
		require.NoError(t, <-errs)
	}
}

func TestRunner_HandlerReceivesRequests(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64
	r := newRunnerForTest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	require.NoError(t, r.Start(context.Background()))
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	for range 5 {
		resp, err := http.Get("http://" + r.Addr().String() + "/")
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
	}
	assert.Equal(t, int64(5), hits.Load())
}

func TestRunner_AddrNilBeforeStart(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.NotFoundHandler())
	assert.Nil(t, r.Addr())
}

func TestRunner_AddrNilAfterShutdown(t *testing.T) {
	t.Parallel()

	r := newRunnerForTest(http.NotFoundHandler())
	require.NoError(t, r.Start(context.Background()))
	require.NotNil(t, r.Addr())
	require.NoError(t, r.Shutdown(context.Background()))
	assert.Nil(t, r.Addr())
}

func TestRunner_ListenError_OnBusyAddr(t *testing.T) {
	t.Parallel()

	first := newRunnerForTest(http.NotFoundHandler())
	require.NoError(t, first.Start(context.Background()))
	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })

	addr := first.Addr().String()

	second := NewRunner("collision", addr, http.NotFoundHandler(), RunnerSettings{
		ReadHeaderTimeout: time.Second,
	}, newRunnerTestLogger())
	err := second.Start(context.Background())
	assert.Error(t, err)
}
