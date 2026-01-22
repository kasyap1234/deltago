package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.API.Testnet {
		t.Error("Default config should not be testnet")
	}
	if len(cfg.Strategy.Underlyings) != 2 {
		t.Error("Default should have BTC and ETH")
	}
	if cfg.Risk.StopLossMultiplier != 1.5 {
		t.Error("Default stop loss should be 1.5")
	}
}

func TestValidate(t *testing.T) {
	cfg := Default()
	// Should fail because API key/secret are empty in Default() (Wait, Default() doesn't set them)
	// Check Default() impl:
	/*
	func Default() *Config {
		return &Config{
			API: APIConfig{ ... no key/secret ... }
		}
	}
	*/
	// So Validate() should fail on Default()
	if err := cfg.Validate(); err == nil {
		t.Error("Default config should be invalid (missing credentials)")
	}

	cfg.API.APIKey = "key"
	cfg.API.APISecret = "secret"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Valid config returned error: %v", err)
	}

	// Invalid cases
	badCfg := *cfg
	badCfg.Strategy.Underlyings = []string{}
	if err := badCfg.Validate(); err == nil {
		t.Error("Should fail with empty underlyings")
	} else if !strings.Contains(err.Error(), "underlying") {
		t.Errorf("Wrong error message: %v", err)
	}

	badCfg = *cfg
	badCfg.Risk.StopLossMultiplier = -1
	if err := badCfg.Validate(); err == nil {
		t.Error("Should fail with negative stop loss")
	}
}

func TestLoad(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test_config.yaml")

	yamlContent := `
api:
  api_key: "env_key"
  api_secret: "env_secret"
  testnet: true
strategy:
  underlyings: 
    - "SOL"
  straddle:
    enabled: true
    entry_time: "10:00"
    position_size: 5
  iron_condor:
    enabled: false
risk:
  stop_loss_multiplier: 2.0
  max_daily_loss: 500
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.API.APIKey != "env_key" {
		t.Errorf("Expected api_key 'env_key', got '%s'", cfg.API.APIKey)
	}
	if !cfg.API.Testnet {
		t.Error("Expected testnet true")
	}
	if len(cfg.Strategy.Underlyings) != 1 || cfg.Strategy.Underlyings[0] != "SOL" {
		t.Errorf("Unexpected underlyings: %v", cfg.Strategy.Underlyings)
	}
	if !cfg.Strategy.Straddle.Enabled {
		t.Error("Straddle should be enabled")
	}
	if cfg.Strategy.Straddle.PositionSize != 5 {
		t.Errorf("Expected position size 5, got %d", cfg.Strategy.Straddle.PositionSize)
	}
}

func TestLoadEnvExpansion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "env_config.yaml")

	yamlContent := `
api:
  api_key: "${TEST_API_KEY}"
  api_secret: "secret"
strategy:
  underlyings: ["BTC"]
risk:
  stop_loss_multiplier: 1.0
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}

	os.Setenv("TEST_API_KEY", "my_secret_key")
	defer os.Unsetenv("TEST_API_KEY")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.API.APIKey != "my_secret_key" {
		t.Errorf("Env var not expanded, got '%s'", cfg.API.APIKey)
	}
}

func TestGetEntryTime(t *testing.T) {
	// Try to load IST, if fails (e.g. on CI without tzdata), skip or handle
	_, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("Asia/Kolkata timezone not available")
	}

	cfg := StraddleConfig{EntryTime: "15:30"}
	entryTime, err := cfg.GetEntryTime()
	if err != nil {
		t.Fatalf("GetEntryTime failed: %v", err)
	}

	// Should be today at 15:30 IST
	// Verify hour/minute
	h, m, _ := entryTime.Clock()
	if h != 15 || m != 30 {
		t.Errorf("Expected 15:30, got %02d:%02d", h, m)
	}
	
	// Check invalid format
	cfg.EntryTime = "invalid"
	_, err = cfg.GetEntryTime()
	if err == nil {
		t.Error("Should fail with invalid time format")
	}
}
