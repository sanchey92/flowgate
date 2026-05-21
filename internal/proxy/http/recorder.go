package proxyhttp

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

type recorder struct {
	http.ResponseWriter

	status   int
	bytesOut int64
	hijacked bool
}

func (rec *recorder) WriteHeader(code int) {
	if rec.status == 0 && code >= 200 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytesOut += int64(n)
	if err != nil {
		return n, fmt.Errorf("recorder write: %w", err)
	}
	return n, nil
}

func (rec *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rec.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("recorder: underlying ResponseWriter does not support Hijacker")
	}
	conn, bwr, err := h.Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("http hijack: %w", err)
	}
	rec.hijacked = true
	if rec.status == 0 {
		rec.status = http.StatusSwitchingProtocols
	}
	return conn, bwr, nil
}

func (rec *recorder) Unwrap() http.ResponseWriter {
	return rec.ResponseWriter
}
