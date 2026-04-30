package tcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

func PipePair(ctx context.Context, a, b net.Conn, pool BufferPool, idle time.Duration) (int64, int64, error) {
	stop := context.AfterFunc(ctx, func() {
		_ = a.Close()
		_ = b.Close()
	})
	defer stop()

	var (
		bytesAB, bytesBA int64
		errAB, errBA     error
		wg               sync.WaitGroup
	)
	wg.Add(2)

	go func() {
		defer wg.Done()
		bytesAB, errAB = pipe(b, a, pool, idle)
		_ = CloseWrite(b)
	}()

	go func() {
		defer wg.Done()
		bytesBA, errBA = pipe(a, b, pool, idle)
		_ = CloseWrite(a)
	}()

	wg.Wait()

	return bytesAB, bytesBA, errors.Join(errAB, errBA)
}

func pipe(dst, src net.Conn, pool BufferPool, idle time.Duration) (int64, error) {
	bp := pool.Get()
	defer pool.Put(bp)

	var (
		r io.Reader = src
		w io.Writer = dst
	)
	if idle > 0 {
		r = &idleReader{c: src, idle: idle}
		w = &idleWriter{c: dst, idle: idle}
	}

	n, err := io.CopyBuffer(w, r, *bp)
	if IsBenignClose(err) {
		return n, nil
	}
	return n, fmt.Errorf("tcp: pipe: %w", err)
}

type idleReader struct {
	c    net.Conn
	idle time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	_ = r.c.SetReadDeadline(time.Now().Add(r.idle))
	count, err := r.c.Read(p)
	if err != nil {
		return 0, fmt.Errorf("reader error: %w", err)
	}
	return count, nil
}

type idleWriter struct {
	c    net.Conn
	idle time.Duration
}

func (w *idleWriter) Write(p []byte) (int, error) {
	_ = w.c.SetWriteDeadline(time.Now().Add(w.idle))
	count, err := w.c.Write(p)
	if err != nil {
		return 0, fmt.Errorf("writer error: %w", err)
	}
	return count, nil
}
