package proxyproto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

const v2SigLen = 12

var sigV2 = [v2SigLen]byte{
	0x0D, 0x0A, 0x0D, 0x0A, 0x00,
	0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
}

const (
	v2HeaderLen = 16

	v2Version = 0x2

	v2CmdLocal = 0x0
	v2CmdProxy = 0x1

	v2FamUnspec = 0x0
	v2FamInet   = 0x1
	v2FamInet6  = 0x2
	v2FamUnix   = 0x3

	v2TransUnspec = 0x0
	v2TransStream = 0x1
	v2TransDgram  = 0x2

	v2AddrLenIPv4 = 12
	v2AddrLenIPv6 = 36
)

func parseV2(r *bufio.Reader) (*Header, error) {
	var hdr [v2HeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("proxyproto v2: %w: short fixed header",
				domainErr.ErrInvalidProxyProtocol)
		}
		return nil, fmt.Errorf("proxyproto v2: read fixed header: %w", err)
	}

	if !bytes.Equal(hdr[:v2SigLen], sigV2[:]) {
		return nil, fmt.Errorf("proxyproto v2: %w: bad signature",
			domainErr.ErrInvalidProxyProtocol)
	}

	verCmd := hdr[12]
	version := verCmd >> 4
	cmd := verCmd & 0x0F
	if version != v2Version {
		return nil, fmt.Errorf("proxyproto v2: %w: bad version 0x%x",
			domainErr.ErrInvalidProxyProtocol, version)
	}
	if cmd != v2CmdLocal && cmd != v2CmdProxy {
		return nil, fmt.Errorf("proxyproto v2: %w: bad command 0x%x",
			domainErr.ErrInvalidProxyProtocol, cmd)
	}

	famTrans := hdr[13]
	family := famTrans >> 4
	trans := famTrans & 0x0F

	length := binary.BigEndian.Uint16(hdr[14:16])

	body, err := readV2Body(r, length)
	if err != nil {
		return nil, err
	}

	if cmd == v2CmdLocal || family == v2FamUnspec || trans == v2TransUnspec {
		return &Header{Version: 2, Command: CommandLocal, Transport: TransportUnknown}, nil
	}

	return decodeV2Addrs(family, trans, body)
}

func readV2Body(r *bufio.Reader, length uint16) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("proxyproto v2: %w: short body",
				domainErr.ErrInvalidProxyProtocol)
		}
		return nil, fmt.Errorf("proxyproto v2: read body: %w", err)
	}
	return body, nil
}

func decodeV2Addrs(family, trans byte, body []byte) (*Header, error) {
	h := &Header{Version: 2, Command: CommandProxy}

	switch family {
	case v2FamInet:
		if len(body) < v2AddrLenIPv4 {
			return nil, fmt.Errorf("proxyproto v2: %w: ipv4 body too short: %d",
				domainErr.ErrInvalidProxyProtocol, len(body))
		}
		var s4, d4 [4]byte
		copy(s4[:], body[0:4])
		copy(d4[:], body[4:8])
		h.Src = netip.AddrPortFrom(netip.AddrFrom4(s4), binary.BigEndian.Uint16(body[8:10]))
		h.Dst = netip.AddrPortFrom(netip.AddrFrom4(d4), binary.BigEndian.Uint16(body[10:12]))

	case v2FamInet6:
		if len(body) < v2AddrLenIPv6 {
			return nil, fmt.Errorf("proxyproto v2: %w: ipv6 body too short: %d",
				domainErr.ErrInvalidProxyProtocol, len(body))
		}
		var s6, d6 [16]byte
		copy(s6[:], body[0:16])
		copy(d6[:], body[16:32])
		h.Src = netip.AddrPortFrom(netip.AddrFrom16(s6), binary.BigEndian.Uint16(body[32:34]))
		h.Dst = netip.AddrPortFrom(netip.AddrFrom16(d6), binary.BigEndian.Uint16(body[34:36]))

	case v2FamUnix:
		return nil, fmt.Errorf("proxyproto v2: %w: AF_UNIX",
			domainErr.ErrUnsupportedProtoFamily)

	default:
		return nil, fmt.Errorf("proxyproto v2: %w: bad family 0x%x",
			domainErr.ErrInvalidProxyProtocol, family)
	}

	switch trans {
	case v2TransStream:
		h.Transport = TransportTCP
	case v2TransDgram:
		h.Transport = TransportUDP
	default:
		return nil, fmt.Errorf("proxyproto v2: %w: bad transport 0x%x",
			domainErr.ErrInvalidProxyProtocol, trans)
	}

	return h, nil
}
