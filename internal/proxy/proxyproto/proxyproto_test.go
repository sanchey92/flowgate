package proxyproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	domainErr "github.com/sanchey92/flowgate/internal/domain/errors"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeOff, false},
		{"off", ModeOff, false},
		{"OFF", ModeOff, false},
		{"v1", ModeV1, false},
		{"V1", ModeV1, false},
		{"v2", ModeV2, false},
		{"V2", ModeV2, false},
		{"auto", ModeAuto, false},
		{"AUTO", ModeAuto, false},
		{"v3", 0, true},
		{"unknown", 0, true},
		{" v1", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseMode(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseMode(%q): err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ParseMode(%q): got=%v, want=%v", tt.in, got, tt.want)
			}
		})
	}
}

func TestHeaderIsLocal(t *testing.T) {
	tests := []struct {
		name string
		h    Header
		want bool
	}{
		{"local", Header{Command: CommandLocal}, true},
		{"proxy", Header{Command: CommandProxy}, false},
		{"zero value is local", Header{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.h.IsLocal(); got != tt.want {
				t.Fatalf("IsLocal: got=%v, want=%v", got, tt.want)
			}
		})
	}
}

func TestDetectAndStrip(t *testing.T) {
	v1Line := "PROXY TCP4 1.2.3.4 5.6.7.8 1234 5678\r\n"
	v2Bytes := buildV2(
		byte(v2Version<<4|v2CmdProxy),
		byte(v2FamInet<<4|v2TransStream),
		ipv4Body([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1234, 5678),
	)
	tail := "PAYLOAD"

	withTail := func(prefix []byte) []byte {
		return append(append([]byte{}, prefix...), []byte(tail)...)
	}

	tests := []struct {
		name      string
		mode      Mode
		input     []byte
		wantNil   bool
		wantVer   int
		wantTail  string
		wantErrIs error
	}{
		{
			name:     "Off ignores v1 prefix and leaves buffer untouched",
			mode:     ModeOff,
			input:    withTail([]byte(v1Line)),
			wantNil:  true,
			wantTail: v1Line + tail,
		},
		{
			name:     "Off ignores v2 prefix and leaves buffer untouched",
			mode:     ModeOff,
			input:    withTail(v2Bytes),
			wantNil:  true,
			wantTail: string(v2Bytes) + tail,
		},
		{
			name:     "V1 with v1 input ok",
			mode:     ModeV1,
			input:    withTail([]byte(v1Line)),
			wantVer:  1,
			wantTail: tail,
		},
		{
			name:      "V1 with v2 input rejects",
			mode:      ModeV1,
			input:     withTail(v2Bytes),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:     "V2 with v2 input ok",
			mode:     ModeV2,
			input:    withTail(v2Bytes),
			wantVer:  2,
			wantTail: tail,
		},
		{
			name:      "V2 with v1 input rejects",
			mode:      ModeV2,
			input:     withTail([]byte(v1Line)),
			wantErrIs: domainErr.ErrInvalidProxyProtocol,
		},
		{
			name:     "Auto detects v1",
			mode:     ModeAuto,
			input:    withTail([]byte(v1Line)),
			wantVer:  1,
			wantTail: tail,
		},
		{
			name:     "Auto detects v2",
			mode:     ModeAuto,
			input:    withTail(v2Bytes),
			wantVer:  2,
			wantTail: tail,
		},
		{
			name:     "Auto without prefix returns nil and keeps buffer",
			mode:     ModeAuto,
			input:    []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"),
			wantNil:  true,
			wantTail: "GET / HTTP/1.1\r\nHost: x\r\n\r\n",
		},
		{
			name:     "Auto with empty input returns nil",
			mode:     ModeAuto,
			input:    nil,
			wantNil:  true,
			wantTail: "",
		},
		{
			name:     "Auto with too few bytes for any signature returns nil",
			mode:     ModeAuto,
			input:    []byte("HEL"),
			wantNil:  true,
			wantTail: "HEL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReaderSize(bytes.NewReader(tt.input), minBuffSize)
			got, err := DetectAndStrip(r, tt.mode)
			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("expected error wrapping %v, got %v", tt.wantErrIs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("want nil header, got %+v", got)
				}
			} else {
				if got == nil {
					t.Fatalf("got nil header, want version %d", tt.wantVer)
				}
				if got.Version != tt.wantVer {
					t.Fatalf("got version %d, want %d", got.Version, tt.wantVer)
				}
			}
			rest, _ := io.ReadAll(r)
			if string(rest) != tt.wantTail {
				t.Fatalf("buffer leftover: got %q, want %q", rest, tt.wantTail)
			}
		})
	}
}

func TestDetectAndStrip_UnknownMode(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(""), minBuffSize)
	_, err := DetectAndStrip(r, Mode(99))
	if err == nil {
		t.Fatalf("expected error for unknown mode, got nil")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestDetectAndStrip_BufioTooSmall(t *testing.T) {
	for _, mode := range []Mode{ModeV1, ModeV2, ModeAuto} {
		t.Run(modeName(mode), func(t *testing.T) {
			r := bufio.NewReaderSize(strings.NewReader(""), minBuffSize-1)
			_, err := DetectAndStrip(r, mode)
			if err == nil {
				t.Fatalf("expected error for too-small bufio")
			}
			if !strings.Contains(err.Error(), "too small") {
				t.Fatalf("expected 'too small' in err, got: %v", err)
			}
		})
	}
}

func TestDetectAndStrip_OffBypassesSizeCheck(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("anything"), minBuffSize-1)
	h, err := DetectAndStrip(r, ModeOff)
	if err != nil || h != nil {
		t.Fatalf("ModeOff: got h=%v err=%v, want nil/nil", h, err)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "anything" {
		t.Fatalf("buffer modified: %q", rest)
	}
}

func modeName(m Mode) string {
	switch m {
	case ModeOff:
		return "off"
	case ModeV1:
		return "v1"
	case ModeV2:
		return "v2"
	case ModeAuto:
		return "auto"
	default:
		return "unknown"
	}
}
