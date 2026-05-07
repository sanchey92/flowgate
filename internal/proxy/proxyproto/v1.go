package proxyproto

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

const (
	maxV1HeaderLen = 107
	sigV1          = "PROXY"
)

func parseV1(r *bufio.Reader) (*Header, error) {
	line := make([]byte, 0, maxV1HeaderLen)
	for {
		b, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("proxyproto v1: %w: EOF before CRLF",
					domainErr.ErrInvalidProxyProtocol)
			}
			return nil, fmt.Errorf("proxyproto v1: read byte: %w", err)
		}
		line = append(line, b)
		n := len(line)
		if n >= 2 && line[n-2] == '\r' && line[n-1] == '\n' {
			return decodeV1line(line[:n-2])
		}
		if n >= maxV1HeaderLen {
			return nil, fmt.Errorf("proxyproto v1: %w: header exceeds %d bytes without CRLF",
				domainErr.ErrInvalidProxyProtocol, maxV1HeaderLen)
		}
	}
}

func decodeV1line(line []byte) (*Header, error) {
	if !bytes.HasPrefix(line, []byte(sigV1+" ")) {
		return nil, fmt.Errorf("proxyproto v1: %w: bad prefix",
			domainErr.ErrInvalidProxyProtocol)
	}

	fields := strings.Split(string(line), " ")
	if len(fields) < 2 {
		return nil, fmt.Errorf("proxyproto v1: %w: bad field count %d",
			domainErr.ErrInvalidProxyProtocol, len(fields))
	}

	proto := fields[1]
	h := &Header{Version: 1}

	switch proto {
	case "TCP4":
		if len(fields) != 6 {
			return nil, fmt.Errorf("proxyproto v1: %w: TCP4 requires 6 fields, got %d",
				domainErr.ErrInvalidProxyProtocol, len(fields))
		}
		return parseV1Addrs(h, fields, true)

	case "TCP6":
		if len(fields) != 6 {
			return nil, fmt.Errorf("proxyproto v1: %w: TCP6 requires 6 fields, got %d",
				domainErr.ErrInvalidProxyProtocol, len(fields))
		}
		return parseV1Addrs(h, fields, false)

	case "UNKNOWN":
		h.Command = CommandLocal
		h.Transport = TransportUnknown
		return h, nil

	default:
		return nil, fmt.Errorf("proxyproto v1: %w: unknown proto %q",
			domainErr.ErrInvalidProxyProtocol, proto)
	}
}

func parseV1Addrs(h *Header, fields []string, expectIPv4 bool) (*Header, error) {
	src, err := netip.ParseAddr(fields[2])
	if err != nil {
		return nil, fmt.Errorf("proxyproto v1: %w: src addr: %w",
			domainErr.ErrInvalidProxyProtocol, err)
	}
	dst, err := netip.ParseAddr(fields[3])
	if err != nil {
		return nil, fmt.Errorf("proxyproto v1: %w: dst addr: %w",
			domainErr.ErrInvalidProxyProtocol, err)
	}

	if expectIPv4 != src.Is4() || expectIPv4 != dst.Is4() {
		return nil, fmt.Errorf("proxyproto v1: %w: addr family mismatch with proto",
			domainErr.ErrInvalidProxyProtocol)
	}

	sp, err := parsePort(fields[4])
	if err != nil {
		return nil, fmt.Errorf("proxyproto v1: %w: src port: %w",
			domainErr.ErrInvalidProxyProtocol, err)
	}
	dp, err := parsePort(fields[5])
	if err != nil {
		return nil, fmt.Errorf("proxyproto v1: %w: dst port: %w",
			domainErr.ErrInvalidProxyProtocol, err)
	}

	h.Command = CommandProxy
	h.Transport = TransportTCP
	h.Src = netip.AddrPortFrom(src, sp)
	h.Dst = netip.AddrPortFrom(dst, dp)

	return h, nil
}

func parsePort(s string) (uint16, error) {
	if len(s) == 0 || len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("parse port: invalid format")
	}
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("parse port: %w", err)
	}
	return uint16(n), nil
}
