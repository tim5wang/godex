package conversation

import (
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
)

// NewCallerForProfile creates a model caller for one configured provider profile.
func NewCallerForProfile(profile config.ModelProfileConfig) Caller {
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	switch strings.ToLower(strings.TrimSpace(profile.Provider)) {
	case config.ProviderOpenAICompatible:
		return NewOpenAIClient(profile.BaseURL, profile.APIKey, timeout)
	case config.ProviderOpenAICodex:
		return NewOpenAICodexClient(profile.BaseURL, profile.APIKey, timeout)
	default:
		return NewClient(profile.BaseURL, profile.APIKey, timeout)
	}
}
