package proxyproto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"testing"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

func buildV2(verCmd, famTrans byte, body []byte) []byte {
	out := make([]byte, 0, v2HeaderLen+len(body))
	out = append(out, sigV2[:]...)
	out = append(out, verCmd, famTrans)
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	out = append(out, body...)
	return out
}

func ipv4Body(s, d [4]byte, sp, dp uint16) []byte {
	b := make([]byte, 0, v2AddrLenIPv4)
	b = append(b, s[:]...)
	b = append(b, d[:]...)
	b = binary.BigEndian.AppendUint16(b, sp)
	b = binary.BigEndian.AppendUint16(b, dp)
	return b
}

func ipv6Body(s, d [16]byte, sp, dp uint16) []byte {
	b := make([]byte, 0, v2AddrLenIPv6)
	b = append(b, s[:]...)
	b = append(b, d[:]...)
	b = binary.BigEndian.AppendUint16(b, sp)
	b = binary.BigEndian.AppendUint16(b, dp)
	return b
}

func TestDecodeV2Addrs(t *testing.T) {
	mustAP := func(s string) netip.AddrPort {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			t.Fatalf("bad test data %q: %v", s, err)
		}
		return ap
	}

	tests := []struct {
		name      string
		family    byte
		trans     byte
		body      []byte
		want      *Header
		wantErrIs error
	}{
		{
			name:   "IPv4 STREAM ok",
			family: v2FamInet,
			trans:  v2TransStream,
			body:   ipv4Body([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1234, 5678),
			want: &Header{
				Version:   2,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       mustAP("1.2.3.4:1234"),
				Dst:       mustAP("5.6.7.8:5678"),
			},
		},
		{
			name:   "IPv4 DGRAM ok",
			family: v2FamInet,
			trans:  v2TransDgram,
			body:   ipv4Body([4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2}, 53, 5353),
			want: &Header{
				Version:   2,
				Command:   CommandProxy,
				Transport: TransportUDP,
				Src:       mustAP("10.0.0.1:53"),
				Dst:       mustAP("10.0.0.2:5353"),
			},
		},
		{
			name:   "IPv4 boundary ports",
			family: v2FamInet,
			trans:  v2TransStream,
			body:   ipv4Body([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 0, 65535),
			want: &Header{
				Version:   2,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       mustAP("1.2.3.4:0"),
				Dst:       mustAP("5.6.7.8:65535"),
			},
		},
		{
			name:   "IPv4 with TLV trailing bytes ignored",
			family: v2FamInet,
			trans:  v2TransStream,
			body: append(
				ipv4Body([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1234, 5678),
				0x01, 0x02, 0x00, 0x00, 0xAA, 0xBB,
			),
			want: &Header{
				Version:   2,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       mustAP("1.2.3.4:1234"),
				Dst:       mustAP("5.6.7.8:5678"),
			},
		},
		{
			name:   "IPv6 STREAM ok",
			family: v2FamInet6,
			trans:  v2TransStream,
			body: ipv6Body(
				netip.MustParseAddr("2001:db8::1").As16(),
				netip.MustParseAddr("2001:db8::2").As16(),
				1234, 5678,
			),
			want: &Header{
				Version:   2,
				Command:   CommandProxy,
				Transport: TransportTCP,
				Src:       mustAP("[2001:db8::1]:1234"),
				Dst:       mustAP("[2001:db8::2]:5678"),
			},
		},
		{
			name:   "IPv6 DGRAM ok",
			family: v2FamInet6,
			trans:  v2TransDgram,
			body: ipv6Body(
				netip.MustParseAddr("::1").As16(),
				netip.MustParseAddr("::2").As16(),
				53, 5353,
			),
			want: &Header{
				Version:   2,
				Command:   CommandProxy,
				Transport: TransportUDP,
				Src:       mustAP("[::1]:53"),
				Dst:       mustAP("[::2]:5353"),
			},
		},
		{
			name:      "IPv4 body too short",
			family:    v2FamInet,
			trans:     v2TransStream,
			body:      make([]byte, v2AddrLenIPv4-1),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "IPv6 body too short",
			family:    v2FamInet6,
			trans:     v2TransStream,
			body:      make([]byte, v2AddrLenIPv6-1),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "AF_UNIX unsupported",
			family:    v2FamUnix,
			trans:     v2TransStream,
			body:      make([]byte, 216),
			wantErrIs: domainErr.ErrUnsupportedProtoFamily,
		},
		{
			name:      "bad family",
			family:    0xF,
			trans:     v2TransStream,
			body:      make([]byte, v2AddrLenIPv4),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "bad transport with IPv4",
			family:    v2FamInet,
			trans:     0xF,
			body:      ipv4Body([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1, 2),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeV2Addrs(tt.family, tt.trans, tt.body)
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

func TestParseV2(t *testing.T) {
	mustAP := func(s string) netip.AddrPort {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			t.Fatalf("bad test data %q: %v", s, err)
		}
		return ap
	}

	const (
		verProxy = byte(v2Version<<4 | v2CmdProxy)
		verLocal = byte(v2Version<<4 | v2CmdLocal)

		famInetStream   = byte(v2FamInet<<4 | v2TransStream)
		famInetDgram    = byte(v2FamInet<<4 | v2TransDgram)
		famInet6Stream  = byte(v2FamInet6<<4 | v2TransStream)
		famInetUnspec   = byte(v2FamInet<<4 | v2TransUnspec)
		famUnspecStream = byte(v2FamUnspec<<4 | v2TransStream)
		famUnspecPair   = byte(0x00)
		famUnixStream   = byte(v2FamUnix<<4 | v2TransStream)
	)

	ipv4 := ipv4Body([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1234, 5678)
	ipv6 := ipv6Body(
		netip.MustParseAddr("2001:db8::1").As16(),
		netip.MustParseAddr("2001:db8::2").As16(),
		1234, 5678,
	)
	tlv := []byte{0x01, 0x02, 0x00, 0x00, 0xAA, 0xBB}
	tail := "PAYLOAD"

	tests := []struct {
		name      string
		input     []byte
		want      *Header
		wantTail  string
		wantErrIs error
	}{
		{
			name:  "TCP4 with payload after header",
			input: append(buildV2(verProxy, famInetStream, ipv4), []byte(tail)...),
			want: &Header{
				Version: 2, Command: CommandProxy, Transport: TransportTCP,
				Src: mustAP("1.2.3.4:1234"), Dst: mustAP("5.6.7.8:5678"),
			},
			wantTail: tail,
		},
		{
			name:  "UDP4",
			input: buildV2(verProxy, famInetDgram, ipv4),
			want: &Header{
				Version: 2, Command: CommandProxy, Transport: TransportUDP,
				Src: mustAP("1.2.3.4:1234"), Dst: mustAP("5.6.7.8:5678"),
			},
		},
		{
			name:  "TCP6",
			input: buildV2(verProxy, famInet6Stream, ipv6),
			want: &Header{
				Version: 2, Command: CommandProxy, Transport: TransportTCP,
				Src: mustAP("[2001:db8::1]:1234"), Dst: mustAP("[2001:db8::2]:5678"),
			},
		},
		{
			name: "TCP4 with TLV tail and payload after",
			input: append(
				buildV2(verProxy, famInetStream, append(append([]byte{}, ipv4...), tlv...)),
				[]byte(tail)...,
			),
			want: &Header{
				Version: 2, Command: CommandProxy, Transport: TransportTCP,
				Src: mustAP("1.2.3.4:1234"), Dst: mustAP("5.6.7.8:5678"),
			},
			wantTail: tail,
		},
		{
			name:  "LOCAL command, body present but ignored",
			input: append(buildV2(verLocal, famInetStream, ipv4), []byte(tail)...),
			want: &Header{
				Version: 2, Command: CommandLocal, Transport: TransportUnknown,
			},
			wantTail: tail,
		},
		{
			name:  "LOCAL command, no body",
			input: append(buildV2(verLocal, famUnspecPair, nil), []byte(tail)...),
			want: &Header{
				Version: 2, Command: CommandLocal, Transport: TransportUnknown,
			},
			wantTail: tail,
		},
		{
			name:  "PROXY+UNSPEC family -> Local (tolerant)",
			input: append(buildV2(verProxy, famUnspecStream, nil), []byte(tail)...),
			want: &Header{
				Version: 2, Command: CommandLocal, Transport: TransportUnknown,
			},
			wantTail: tail,
		},
		{
			name:  "PROXY+UNSPEC transport -> Local (tolerant)",
			input: append(buildV2(verProxy, famInetUnspec, nil), []byte(tail)...),
			want: &Header{
				Version: 2, Command: CommandLocal, Transport: TransportUnknown,
			},
			wantTail: tail,
		},
		{
			name:      "AF_UNIX unsupported",
			input:     buildV2(verProxy, famUnixStream, make([]byte, 216)),
			wantErrIs: domainErr.ErrUnsupportedProtoFamily,
		},
		{
			name: "bad signature",
			input: append(
				bytes.Repeat([]byte{0xFF}, v2SigLen),
				verProxy, famInetStream, 0x00, 0x00,
			),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "bad version",
			input:     buildV2(0x31, famInetStream, ipv4),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "bad command",
			input:     buildV2(0x22, famInetStream, ipv4),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "short fixed header (EOF mid-signature)",
			input:     sigV2[:5],
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "short body (EOF mid-body)",
			input:     buildV2(verProxy, famInetStream, ipv4)[:v2HeaderLen+5],
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "IPv4 length too short",
			input:     buildV2(verProxy, famInetStream, make([]byte, v2AddrLenIPv4-1)),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:      "empty",
			input:     nil,
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(tt.input))
			got, err := parseV2(r)
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

func TestParseV2_ChunkedReads(t *testing.T) {
	header := buildV2(
		byte(v2Version<<4|v2CmdProxy),
		byte(v2FamInet<<4|v2TransStream),
		ipv4Body([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1234, 5678),
	)
	tail := "after-header"
	full := string(header) + tail

	for _, chunk := range []int{1, 3, 7, 16, 37, 64} {
		t.Run("chunk="+strconv.Itoa(chunk), func(t *testing.T) {
			r := bufio.NewReader(&chunkedReader{data: full, chunkSz: chunk})
			h, err := parseV2(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.Version != 2 || h.Src.Port() != 1234 || h.Dst.Port() != 5678 {
				t.Fatalf("unexpected header: %+v", h)
			}
			rest, _ := io.ReadAll(r)
			if string(rest) != tail {
				t.Fatalf("tail mismatch: got %q, want %q", rest, tail)
			}
		})
	}
}
