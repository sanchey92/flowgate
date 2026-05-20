package proxyhttp

import (
	"fmt"
	"net/http"
)

type recorder struct {
	http.ResponseWriter

	status   int
	bytesOut int64
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

func (rec *recorder) Unwrap() http.ResponseWriter {
	return rec.ResponseWriter
}
