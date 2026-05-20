package requestid

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"time"
)

func New() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("err-%d", time.Now().UnixNano())
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
