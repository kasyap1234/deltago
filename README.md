# Delta Exchange India Trading Bot

A Go-based **adaptive** automated trading bot for Delta Exchange India that dynamically selects options strategies based on market regime detection.

## 🎯 Adaptive Strategy System

Unlike single-strategy bots that fail in certain market conditions, this bot **adapts** to the current market regime:

| Market Condition | Detected Regime | Strategy Selected |
|------------------|-----------------|-------------------|
| **Uptrend** | Trend: UP | Bull Call Spread |
| **Downtrend** | Trend: DOWN | Bear Put Spread |
| **Sideways + High Vol** | Sideways + VolHigh | Iron Condor (sell premium) |
| **Sideways + Low Vol** | Sideways + VolLow | Long Straddle (buy vol) |
| **Crash/Stress** | StressCrash | Protective Put |

## Features

- 🧠 **Regime Detection**: Automatically detects trend, volatility, and crash conditions
- 📊 **5 Strategies**: Bull Call Spread, Bear Put Spread, Iron Condor, Long Straddle, Protective Put
- 🔄 **Adaptive Selection**: Picks optimal strategy based on current regime
- 💰 **Lowest Fees**: Uses `post_only=true` for maker fees (0.01%)
- 🛡️ **Risk Management**: Portfolio-level Greeks limits, daily loss cap, position reconciliation
- ✅ **Fill Verification**: Proper order execution with fill tracking
- 🦺 **Defined Risk**: All strategies have known max loss (no naked shorts)

## Quick Start

### 1. Set Environment Variables

```bash
export DELTA_API_KEY="your_api_key"
export DELTA_API_SECRET="your_api_secret"
```

### 2. Build

```bash
# Build adaptive bot (recommended)
go build -o adaptive ./cmd/adaptive

# Build legacy single-strategy bot
go build -o bot ./cmd/bot
```

### 3. Run

```bash
# Adaptive bot - automatically selects strategies based on market conditions
./adaptive -config config.yaml

# Dry run (monitor regime without trading)
./adaptive -config config.yaml -dry-run

# Specify underlying
./adaptive -config config.yaml -underlying BTC
```

### Legacy Bot (Single Strategy)

```bash
./bot -config config.yaml -force-entry
```

## Configuration

Edit `config.yaml`:

```yaml
api:
  base_url: "https://api.india.delta.exchange"
  websocket_url: "wss://socket.india.delta.exchange"
  api_key: "${DELTA_API_KEY}"
  api_secret: "${DELTA_API_SECRET}"
  testnet: false  # Set to true for testing

strategy:
  underlyings:
    - "BTC"
    - "ETH"
  straddle:
    enabled: true
    entry_time: "11:30"  # IST
    position_size: 1
  iron_condor:
    enabled: true
    short_delta: 0.25
    wing_width: 2
    position_size: 1

risk:
  stop_loss_multiplier: 1.5
  max_daily_loss: 1000
```

## Strategies

### Regime Detection

The bot classifies market conditions into three dimensions:

1. **Trend**: UP, DOWN, or SIDEWAYS (based on EMA slopes, ADX)
2. **Volatility**: HIGH, LOW, or NORMAL (based on ATR, IV rank)
3. **Stress**: NORMAL or CRASH (based on drawdown, vol spikes)

### Bull Call Spread (Uptrend)
- Buy ATM call + Sell OTM call
- Profits from moderate upward moves
- Max loss = debit paid

### Bear Put Spread (Downtrend)
- Buy ATM put + Sell OTM put
- Profits from moderate downward moves
- Max loss = debit paid

### Iron Condor (Sideways + High Vol)
- Sell OTM call + put at ~0.25 delta
- Buy protective wings further OTM
- Profits from range-bound markets
- Max loss = spread width - credit

### Long Straddle (Sideways + Low Vol)
- Buy ATM call + put
- Profits from large moves in either direction
- Best when IV is low and expected to expand

### Protective Put (Crash)
- Buy OTM put for crash protection
- Profits from market crashes
- Acts as portfolio insurance

## Project Structure

```
deltago/
├── cmd/
│   ├── adaptive/main.go      # Adaptive bot entry point (recommended)
│   ├── bot/main.go           # Legacy single-strategy bot
│   └── backtest/main.go      # Backtesting tool
├── internal/
│   ├── bot/adaptive.go       # Adaptive bot core
│   ├── regime/               # Market regime detection
│   │   ├── types.go          # Regime types
│   │   └── detector.go       # Trend/vol/crash detection
│   ├── strategies/           # Options strategies
│   │   ├── interface.go      # Strategy interface
│   │   ├── bull_call_spread.go
│   │   ├── bear_put_spread.go
│   │   ├── iron_condor.go
│   │   ├── long_straddle.go
│   │   └── protective_put.go
│   ├── selector/selector.go  # Regime → Strategy mapping
│   ├── execution/            # Order execution with fill verification
│   ├── portfolio/types.go    # Portfolio state & Greeks
│   ├── delta/                # Delta Exchange API client
│   └── config/config.go      # Configuration
├── config.yaml               # Configuration
└── go.mod                    # Dependencies
```

## API Keys

1. Go to [Delta Exchange India](https://www.delta.exchange/app/account/manageapikeys)
2. Create API key with **Trading** permission
3. Whitelist your server's IP address
4. Set environment variables

## Testing

Use testnet first:

```yaml
api:
  testnet: true
```

Testnet URL: `https://cdn-ind.testnet.deltaex.org`

## Risk Disclaimer

⚠️ **This bot trades real money.** Use at your own risk. Start with small position sizes and test thoroughly on testnet before production use.

## License

MIT
