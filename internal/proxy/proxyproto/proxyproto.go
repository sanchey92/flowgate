package proxyproto

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"strings"
)

type Mode int

const (
	ModeOff Mode = iota
	ModeV1
	ModeV2
	ModeAuto
)

type Command uint8

const (
	CommandLocal Command = 0x0
	CommandProxy Command = 0x1
)

type Transport string

const (
	TransportUnknown Transport = ""
	TransportTCP     Transport = "tcp"
	TransportUDP     Transport = "udp"
)

type Header struct {
	Version   int
	Command   Command
	Transport Transport
	Src       netip.AddrPort
	Dst       netip.AddrPort
}

func (h *Header) IsLocal() bool {
	return h.Command == CommandLocal
}

const minBuffSize = 1024

func DetectAndStrip(r *bufio.Reader, mode Mode) (*Header, error) {
	if mode == ModeOff {
		return nil, nil
	}

	if r.Size() < minBuffSize {
		return nil, fmt.Errorf("proxyproto: bufio reader too small: %d < %d",
			r.Size(), minBuffSize)
	}

	switch mode {
	case ModeOff:
		return nil, nil
	case ModeV1:
		return parseV1(r)
	case ModeV2:
		return parseV2(r)
	case ModeAuto:
		return detectAuto(r)
	default:
		return nil, fmt.Errorf("proxyproto: unknown mode %d", mode)
	}
}

func detectAuto(r *bufio.Reader) (*Header, error) {
	if peek, err := r.Peek(v2SigLen); err == nil && bytes.Equal(peek, sigV2[:]) {
		return parseV2(r)
	}

	if peek, err := r.Peek(len(sigV1)); err == nil && string(peek) == sigV1 {
		return parseV1(r)
	}

	return nil, nil
}

func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(s) {
	case "", "off":
		return ModeOff, nil
	case "v1":
		return ModeV1, nil
	case "v2":
		return ModeV2, nil
	case "auto":
		return ModeAuto, nil
	default:
		return 0, fmt.Errorf("proxyproto: unknown mode: %q", s)
	}
}
