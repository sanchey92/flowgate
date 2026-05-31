package passive

import (
	"fmt"

	"github.com/sanchey92/flowgate/internal/domain/model"
)

func Attach(backends []*model.Backend, cfg *Config) error {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return fmt.Errorf("passive: validate config: %w", err)
	}
	for _, b := range backends {
		b.AttachBreaker(NewBreaker(cfg))
	}
	return nil
}
