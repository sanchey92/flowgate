package tcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

func PipePair(ctx context.Context, a, b net.Conn, pool BufferPool) (int64, int64, error) {
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
		bytesAB, errAB = pipe(b, a, pool)
		_ = CloseWrite(b)
	}()

	go func() {
		defer wg.Done()
		bytesBA, errBA = pipe(a, b, pool)
		_ = CloseWrite(a)
	}()

	wg.Wait()
	_ = a.Close()
	_ = b.Close()

	return bytesAB, bytesBA, errors.Join(errAB, errBA)
}

func pipe(dst io.Writer, src io.Reader, pool BufferPool) (int64, error) {
	bp := pool.Get()
	defer pool.Put(bp)

	n, err := io.CopyBuffer(dst, src, *bp)
	if IsBenignClose(err) {
		return n, nil
	}
	return n, fmt.Errorf("tcp: pipe: %w", err)
}
