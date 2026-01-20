package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the trading bot
type Config struct {
	API      APIConfig      `yaml:"api"`
	Strategy StrategyConfig `yaml:"strategy"`
	Risk     RiskConfig     `yaml:"risk"`
}

// APIConfig holds Delta Exchange API configuration
type APIConfig struct {
	BaseURL      string `yaml:"base_url"`
	WebSocketURL string `yaml:"websocket_url"`
	APIKey       string `yaml:"api_key"`
	APISecret    string `yaml:"api_secret"`
	Testnet      bool   `yaml:"testnet"`
}

// StrategyConfig holds trading strategy configuration
type StrategyConfig struct {
	Underlyings []string        `yaml:"underlyings"`
	Straddle    StraddleConfig  `yaml:"straddle"`
	IronCondor  IronCondorConfig `yaml:"iron_condor"`
}

// StraddleConfig holds daily straddle strategy settings
type StraddleConfig struct {
	Enabled      bool   `yaml:"enabled"`
	EntryTime    string `yaml:"entry_time"` // Format: "HH:MM" in IST
	PositionSize int    `yaml:"position_size"`
}

// IronCondorConfig holds iron condor strategy settings
type IronCondorConfig struct {
	Enabled      bool    `yaml:"enabled"`
	ShortDelta   float64 `yaml:"short_delta"`
	WingWidth    int     `yaml:"wing_width"` // Number of strikes for protection
	PositionSize int     `yaml:"position_size"`
}

// RiskConfig holds risk management settings
type RiskConfig struct {
	StopLossMultiplier float64 `yaml:"stop_loss_multiplier"`
	MaxDailyLoss       float64 `yaml:"max_daily_loss"`
}

// Load reads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.API.APIKey == "" {
		return fmt.Errorf("api_key is required")
	}
	if c.API.APISecret == "" {
		return fmt.Errorf("api_secret is required")
	}
	if len(c.Strategy.Underlyings) == 0 {
		return fmt.Errorf("at least one underlying asset is required")
	}
	if c.Risk.StopLossMultiplier <= 0 {
		return fmt.Errorf("stop_loss_multiplier must be positive")
	}
	return nil
}

// GetEntryTime parses the entry time string and returns today's entry time in IST
func (c *StraddleConfig) GetEntryTime() (time.Time, error) {
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to load IST timezone: %w", err)
	}

	now := time.Now().In(ist)
	entryTime, err := time.Parse("15:04", c.EntryTime)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid entry_time format: %w", err)
	}

	return time.Date(
		now.Year(), now.Month(), now.Day(),
		entryTime.Hour(), entryTime.Minute(), 0, 0, ist,
	), nil
}

// Default returns a default configuration
func Default() *Config {
	return &Config{
		API: APIConfig{
			BaseURL:      "https://api.india.delta.exchange",
			WebSocketURL: "wss://socket.india.delta.exchange",
			Testnet:      false,
		},
		Strategy: StrategyConfig{
			Underlyings: []string{"BTC", "ETH"},
			Straddle: StraddleConfig{
				Enabled:      true,
				EntryTime:    "11:30",
				PositionSize: 1,
			},
			IronCondor: IronCondorConfig{
				Enabled:      true,
				ShortDelta:   0.25,
				WingWidth:    2,
				PositionSize: 1,
			},
		},
		Risk: RiskConfig{
			StopLossMultiplier: 1.5,
			MaxDailyLoss:       1000,
		},
	}
}
