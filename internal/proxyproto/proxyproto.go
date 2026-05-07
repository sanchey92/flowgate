package proxyproto

import (
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
