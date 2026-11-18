package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearKBDownloadEnv unsets every guard knob so the test observes the defaults.
func clearKBDownloadEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"KB_DOWNLOAD_DAILY_LIMIT",
		"KB_DOWNLOAD_WINDOW_MINUTES",
		"KB_DOWNLOAD_WINDOW_LIMIT",
		"KB_DOWNLOAD_SUSPEND_HOURS",
	} {
		t.Setenv(key, "")
	}
}

// A deployment that never sets KB_DOWNLOAD_* must still get a working guard.
// The regression this pins: an all-zero section silently disabled the quota and
// the burst freeze while the counters kept counting.
func TestKBDownloadDefaultsWhenNothingIsConfigured(t *testing.T) {
	clearKBDownloadEnv(t)

	cfg := &Config{}
	applyKBDownloadEnvOverrides(cfg)

	require.NotNil(t, cfg.KBDownload)
	assert.Equal(t, DefaultKBDownloadDailyLimit, cfg.KBDownload.DailyLimit)
	assert.Equal(t, DefaultKBDownloadWindowMinutes, cfg.KBDownload.WindowMinutes)
	assert.Equal(t, DefaultKBDownloadWindowLimit, cfg.KBDownload.WindowLimit)
	assert.Equal(t, DefaultKBDownloadSuspendHours, cfg.KBDownload.SuspendHours)
}

// One variable overrides one knob; the siblings keep their defaults.
func TestKBDownloadEnvOverridesASingleKnob(t *testing.T) {
	clearKBDownloadEnv(t)
	t.Setenv("KB_DOWNLOAD_DAILY_LIMIT", "5")

	cfg := &Config{}
	applyKBDownloadEnvOverrides(cfg)

	assert.Equal(t, 5, cfg.KBDownload.DailyLimit)
	assert.Equal(t, DefaultKBDownloadWindowLimit, cfg.KBDownload.WindowLimit)
	assert.Equal(t, DefaultKBDownloadSuspendHours, cfg.KBDownload.SuspendHours)
}

// An explicit 0 stays meaningful: it is how an operator turns one rule off.
func TestKBDownloadExplicitZeroDisablesOneRule(t *testing.T) {
	clearKBDownloadEnv(t)
	t.Setenv("KB_DOWNLOAD_DAILY_LIMIT", "0")

	cfg := &Config{}
	applyKBDownloadEnvOverrides(cfg)

	assert.Equal(t, 0, cfg.KBDownload.DailyLimit)
	assert.Equal(t, DefaultKBDownloadWindowLimit, cfg.KBDownload.WindowLimit)
}

// A section declared in config.yaml wins verbatim, including zeros: the env
// layer must not fill it back in with defaults.
func TestKBDownloadYAMLSectionWinsOverDefaults(t *testing.T) {
	clearKBDownloadEnv(t)

	cfg := &Config{KBDownload: &KBDownloadLimitConfig{WindowMinutes: 2}}
	applyKBDownloadEnvOverrides(cfg)

	assert.Equal(t, 0, cfg.KBDownload.DailyLimit)
	assert.Equal(t, 2, cfg.KBDownload.WindowMinutes)
	assert.Equal(t, 0, cfg.KBDownload.WindowLimit)
}
