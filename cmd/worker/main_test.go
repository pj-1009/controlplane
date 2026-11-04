package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	require.NoError(t, err)
	assert.Equal(t, 0.0, cfg.FailRate)
	assert.Equal(t, 500*time.Millisecond, cfg.MinDelay)
	assert.Equal(t, 2000*time.Millisecond, cfg.MaxDelay)
	assert.False(t, cfg.SkipInProgress)
}

func TestFlagWinsOverEnv(t *testing.T) {
	t.Setenv("WORKER_FAIL_RATE", "0.5")
	t.Setenv("WORKER_SKIP_IN_PROGRESS", "true")

	cfg, err := parseFlags(nil)
	require.NoError(t, err)
	assert.Equal(t, 0.5, cfg.FailRate, "env applies without a flag")
	assert.True(t, cfg.SkipInProgress)

	cfg, err = parseFlags([]string{"-fail-rate=1", "-skip-in-progress=false"})
	require.NoError(t, err)
	assert.Equal(t, 1.0, cfg.FailRate, "flag wins")
	assert.False(t, cfg.SkipInProgress)
}

func TestInvalidSettingsAreRejected(t *testing.T) {
	tests := map[string][]string{
		"fail rate above 1":      {"-fail-rate=1.5"},
		"negative fail rate":     {"-fail-rate=-0.1"},
		"duplicate rate above 1": {"-duplicate-rate=2"},
		"negative min delay":     {"-min-delay-ms=-1"},
		"max below min":          {"-min-delay-ms=500", "-max-delay-ms=100"},
		"zero concurrency":       {"-concurrency=0"},
		"not a number":           {"-fail-rate=lots"},
		"unknown flag":           {"-explode"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseFlags(args)
			assert.Error(t, err)
		})
	}

	t.Setenv("WORKER_MIN_DELAY_MS", "soon")
	_, err := parseFlags(nil)
	assert.ErrorContains(t, err, "WORKER_MIN_DELAY_MS")
}
