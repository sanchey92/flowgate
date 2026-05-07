package proxyproto

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

func TestParsePort(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    uint16
		wantErr bool
	}{
		{"zero", "0", 0, false},
		{"min nonzero", "1", 1, false},
		{"typical", "1234", 1234, false},
		{"max", "65535", 65535, false},
		{"overflow", "65536", 0, true},
		{"empty", "", 0, true},
		{"leading zero short", "01", 0, true},
		{"leading zero double", "00", 0, true},
		{"leading zero long", "012", 0, true},
		{"non digit", "abc", 0, true},
		{"negative", "-1", 0, true},
		{"plus sign", "+1", 0, true},
		{"leading space", " 1", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePort(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parsePort(%q): err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parsePort(%q): got=%d, want=%d", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecodeV1Line(t *testing.T) {
	mustAP := func(s string) netip.AddrPort {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			t.Fatalf("bad test data %q: %v", s, err)
		}
		return ap
	}

	tests := []struct {
		name      string
		line      string
		want      *Header
		wantErrIs error
	}{
		{
			name: "TCP4 ok",
			line: "PROXY TCP4 1.2.3.4 5.6.7.8 1234 5678",
			want: &Header{
				Version:   1,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       mustAP("1.2.3.4:1234"),
				Dst:       mustAP("5.6.7.8:5678"),
			},
		},
		{
			name: "TCP4 boundary ports",
			line: "PROXY TCP4 1.2.3.4 5.6.7.8 0 65535",
			want: &Header{
				Version:   1,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       mustAP("1.2.3.4:0"),
				Dst:       mustAP("5.6.7.8:65535"),
			},
		},
		{
			name: "TCP6 ok",
			line: "PROXY TCP6 ::1 ::2 1234 5678",
			want: &Header{
				Version:   1,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       mustAP("[::1]:1234"),
				Dst:       mustAP("[::2]:5678"),
			},
		},
		{
			name: "UNKNOWN minimal",
			line: "PROXY UNKNOWN",
			want: &Header{
				Version:   1,
				Command:   CommandLocal,
				Transport: TransportUnknown,
			},
		},
		{
			name: "UNKNOWN with junk fields per spec",
			line: "PROXY UNKNOWN 1.2.3.4 5.6.7.8 1 2",
			want: &Header{
				Version:   1,
				Command:   CommandLocal,
				Transport: TransportUnknown,
			},
		},
		{
			name: "UNKNOWN with three fields",
			line: "PROXY UNKNOWN whatever",
			want: &Header{
				Version:   1,
				Command:   CommandLocal,
				Transport: TransportUnknown,
			},
		},
		{
			name:      "bad prefix",
			line:      "FOO TCP4 1.2.3.4 5.6.7.8 1 2",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "PROXY only",
			line:      "PROXY",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "unknown proto",
			line:      "PROXY FOOBAR",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP4 wrong field count",
			line:      "PROXY TCP4 1.2.3.4 5.6.7.8 1234",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP6 wrong field count",
			line:      "PROXY TCP6 ::1 ::2 1234",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP4 with IPv6",
			line:      "PROXY TCP4 ::1 ::2 1234 5678",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP6 with IPv4",
			line:      "PROXY TCP6 1.2.3.4 5.6.7.8 1234 5678",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP4 rejects IPv4-mapped IPv6",
			line:      "PROXY TCP4 ::ffff:1.2.3.4 5.6.7.8 1234 5678",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP4 bad src addr",
			line:      "PROXY TCP4 not.an.ip 5.6.7.8 1234 5678",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP4 bad dst addr",
			line:      "PROXY TCP4 1.2.3.4 not.an.ip 1234 5678",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP4 leading zero src port",
			line:      "PROXY TCP4 1.2.3.4 5.6.7.8 01234 5678",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "TCP4 dst port overflow",
			line:      "PROXY TCP4 1.2.3.4 5.6.7.8 1234 65536",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "empty line",
			line:      "",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeV1line([]byte(tt.line))
			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("expected error wrapping %v, got %v", tt.wantErrIs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatalf("got nil header")
			}
			if *got != *tt.want {
				t.Fatalf("header mismatch:\n got=%+v\nwant=%+v", got, tt.want)
			}
		})
	}
}

func TestParseV1(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      *Header
		wantTail  string
		wantErrIs error
	}{
		{
			name:  "valid TCP4 with payload after CRLF",
			input: "PROXY TCP4 1.2.3.4 5.6.7.8 1234 5678\r\nGET / HTTP/1.1\r\n",
			want: &Header{
				Version:   1,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       netip.MustParseAddrPort("1.2.3.4:1234"),
				Dst:       netip.MustParseAddrPort("5.6.7.8:5678"),
			},
			wantTail: "GET / HTTP/1.1\r\n",
		},
		{
			name:  "UNKNOWN minimal",
			input: "PROXY UNKNOWN\r\nDATA",
			want: &Header{
				Version:   1,
				Command:   CommandLocal,
				Transport: TransportUnknown,
			},
			wantTail: "DATA",
		},
		{
			name:      "missing CRLF EOF",
			input:     "PROXY TCP4 1.2.3.4 5.6.7.8 1234 5678",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "empty",
			input:     "",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "CRLF beyond 107 bytes",
			input:     "PROXY TCP4 1.2.3.4 5.6.7.8 1234 5678 " + strings.Repeat("X", 200) + "\r\n",
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.input))
			got, err := parseV1(r)
			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("expected error wrapping %v, got %v", tt.wantErrIs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if *got != *tt.want {
				t.Fatalf("header mismatch:\n got=%+v\nwant=%+v", got, tt.want)
			}
			rest, _ := io.ReadAll(r)
			if string(rest) != tt.wantTail {
				t.Fatalf("buffer leftover: got %q, want %q", rest, tt.wantTail)
			}
		})
	}
}

type chunkedReader struct {
	data    string
	chunkSz int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.chunkSz
	if n > len(c.data) {
		n = len(c.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

func TestParseV1_ChunkedReads(t *testing.T) {
	header := "PROXY TCP4 1.2.3.4 5.6.7.8 1234 5678\r\n"
	tail := "after-header"
	full := header + tail

	for _, chunk := range []int{1, 3, 7, 16, 37, 64} {
		t.Run("chunk="+strconv.Itoa(chunk), func(t *testing.T) {
			r := bufio.NewReader(&chunkedReader{data: full, chunkSz: chunk})
			h, err := parseV1(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.Version != 1 || h.Src.Port() != 1234 || h.Dst.Port() != 5678 {
				t.Fatalf("unexpected header: %+v", h)
			}
			rest, _ := io.ReadAll(r)
			if string(rest) != tail {
				t.Fatalf("tail mismatch: got %q, want %q", rest, tail)
			}
		})
	}
}

func TestParseV1_DoesNotBlockOnExactHeader(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	header := "PROXY TCP4 1.2.3.4 5.6.7.8 1234 5678\r\n"

	writeErr := make(chan error, 1)
	go func() {
		_, err := clientConn.Write([]byte(header))
		writeErr <- err
	}()

	type result struct {
		h   *Header
		err error
	}
	done := make(chan result, 1)
	go func() {
		r := bufio.NewReader(serverConn)
		h, err := parseV1(r)
		done <- result{h, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.h.Src.Port() != 1234 || res.h.Dst.Port() != 5678 {
			t.Fatalf("unexpected header: %+v", res.h)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("parseV1 blocked waiting for bytes after CRLF")
	}

	if err := <-writeErr; err != nil {
		t.Fatalf("client write failed: %v", err)
	}
}
