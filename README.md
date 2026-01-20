# Delta Exchange India Trading Bot

A Go-based automated trading bot for Delta Exchange India with **Daily Straddle** and **Iron Condor** options strategies.

## Features

- 🎯 **Daily Straddle Strategy**: Sells ATM Call + Put at 11:30 AM IST
- 🦅 **Iron Condor Strategy**: Sells OTM options with protective wings
- 💰 **Lowest Fees**: Uses `post_only=true` for maker fees (0.01%)
- 🛡️ **Risk Management**: 1.5x premium stop-loss + max daily loss protection
- 📡 **Real-time Monitoring**: WebSocket-based position updates
- ⏰ **Scheduled Execution**: Automatic entry at configured time

## Quick Start

### 1. Set Environment Variables

```bash
export DELTA_API_KEY="your_api_key"
export DELTA_API_SECRET="your_api_secret"
```

### 2. Build

```bash
go build -o bot ./cmd/bot
```

### 3. Run

```bash
# Normal mode - waits for 11:30 AM IST
./bot -config config.yaml

# Force immediate entry (testing)
./bot -config config.yaml -force-entry

# Dry run (no actual trades)
./bot -config config.yaml -dry-run
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

### Daily Straddle (11:30 AM IST Entry)

1. Gets current BTC/ETH spot price
2. Finds ATM Call and Put expiring at 5:30 PM IST
3. Sells both with `post_only=true` for maker fees
4. Monitors for 1.5x premium stop-loss
5. Positions expire at 5:30 PM IST

### Iron Condor

1. Finds OTM options at ~0.25 delta
2. Buys protective wings 2 strikes further OTM
3. All 4 legs placed with maker flag
4. Max loss is capped at (spread width - net credit)

## Project Structure

```
deltago/
├── cmd/bot/main.go           # Entry point
├── internal/
│   ├── config/config.go      # Configuration
│   ├── delta/
│   │   ├── auth.go           # HMAC authentication
│   │   ├── client.go         # REST client
│   │   ├── models.go         # API types
│   │   ├── orders.go         # Order management
│   │   ├── positions.go      # Position management
│   │   ├── products.go       # Products/options
│   │   └── websocket.go      # Real-time updates
│   ├── strategy/
│   │   ├── straddle.go       # Daily straddle
│   │   └── condor.go         # Iron condor
│   └── risk/manager.go       # Risk management
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
