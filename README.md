# Polymarket Trading Bot

A high-performance, low-latency algorithmic trading bot for Polymarket that connects to real-time WebSocket feeds, analyzes market movements, and automatically executes trades based on configurable strategies.

## Features

- **Low-latency architecture**: Sub-millisecond event processing with optimized goroutines and object pooling
- **Config-driven**: Fully customizable via YAML without code changes
- **Multiple strategies**: Threshold-based, market making, and momentum trading
- **Risk management**: Position limits, daily loss caps, exposure tracking, auto-halt on thresholds
- **Rate limiting**: Smart order batching and per-second/per-minute rate limits
- **HMAC authentication**: Secure API signing for all order submissions
- **Comprehensive metrics**: Prometheus-compatible metrics server with latency tracking
- **Graceful error handling**: Auto-reconnect, retry logic, and position recovery
- **Performance tuning**: GOGC control, CPU core pinning, buffer size optimization

## Architecture

```
┌─────────────┐         ┌──────────────┐         ┌────────────┐
│  WebSocket  │────────▶│   Strategy   │────────▶│   Order    │
│   Client    │         │    Engine    │         │  Manager   │
└─────────────┘         └──────────────┘         └────────────┘
      │                                                  │
      │                                                  ▼
      │                                           ┌────────────┐
      │                                           │ API Client │
      │                                           │ (w/ HMAC)  │
      │                                           └────────────┘
      ▼                                                  │
┌─────────────┐                                         │
│   Metrics   │◀────────────────────────────────────────┘
│  Collector  │
└─────────────┘
```

## Quick Start

### 1. Installation

```bash
# Clone the repository
git clone https://github.com/seeker/polymarket-bot.git
cd polymarket-bot

# Install dependencies
go mod download

# Build the bot
go build -o polymarket-bot ./cmd/bot
```

### 2. Configuration

Copy the example config and customize:

```bash
cp config.example.yml config.yml
```

#### Option A: Automatic Setup (Recommended)

Install Python dependencies and run the setup script to automatically generate API credentials. On Debian/Ubuntu, use a virtual environment to avoid the “externally-managed-environment” pip error:

```bash
# Create and activate a virtual environment (recommended)
python3 -m venv .venv
source .venv/bin/activate

# Install Python dependencies inside the venv
pip install -r requirements.txt

# Edit config.yml and add your EOA wallet private key
# Set api.private_key to your wallet's private key (without 0x prefix)

# Run the setup script
python scripts/setup_credentials.py
```

The script will:
1. Read your private key from `config.yml`
2. Connect to Polymarket CLOB and generate API credentials
3. Automatically update `config.yml` with the generated credentials

#### Option B: Manual Setup

Manually add your API credentials directly to `config.yml`:

```yaml
api:
  api_key: "your_api_key"
  api_secret: "your_api_secret"
  passphrase: "your_passphrase"
  private_key: "your_wallet_private_key"
```

#### Configure Trading Parameters

Edit `config.yml` to configure:
- Target markets to trade
- Strategy parameters (buy/sell thresholds)
- Position sizes and risk limits
- WebSocket and API settings

### 3. Run

```bash
./polymarket-bot -config config.yml
```

## Configuration Guide

### Strategy Parameters

```yaml
strategy:
  buy_below: 0.45        # Buy when price drops below this
  sell_above: 0.55       # Sell when price rises above this
  quantity: 100          # Contracts per order
  max_position_size: 500 # Maximum total position
  mode: "threshold"      # Strategy mode
```

### Risk Management

```yaml
risk:
  max_daily_loss: 1000           # Stop trading after this loss
  max_loss_per_trade: 100        # Maximum loss per trade
  auto_halt_on_loss: true        # Auto-stop on daily loss
  max_exposure_per_market: 5000  # Per-market exposure limit
```

### Performance Tuning

```yaml
performance:
  worker_count: 4          # Goroutine count
  gc_percent: 100          # GOGC value
  cpu_cores: []            # CPU affinity (OS-specific)
  struct_pool_size: 1000   # Object pool size
```

### Monitoring

```yaml
monitoring:
  enabled: true
  metrics_port: 9090       # Prometheus metrics
  track_latency: true
  max_latency_ms: 100      # Alert threshold
  log_level: "info"        # debug/info/warn/error
```

## Metrics & Monitoring

Access metrics at `http://localhost:9090/metrics`:

- `polymarket_messages_received_total` - WebSocket messages
- `polymarket_orders_submitted_total` - Orders submitted
- `polymarket_orders_successful_total` - Successful orders
- `polymarket_trades_executed_total` - Executed trades
- `polymarket_latency_avg_ms` - Average latency
- `polymarket_pnl_total` - Total P&L
- `polymarket_pnl_daily` - Daily P&L

## Project Structure

```
.
├── cmd/
│   └── bot/              # Main entry point
├── internal/
│   ├── api/              # API client with HMAC signing
│   ├── config/           # Configuration loader
│   ├── metrics/          # Metrics collection
│   ├── order/            # Order manager with rate limiting
│   ├── strategy/         # Strategy engine
│   └── websocket/        # WebSocket client
├── pkg/
│   └── types/            # Shared types
├── scripts/
│   └── setup_credentials.py  # API credentials setup script
├── config.example.yml    # Example configuration
├── requirements.txt      # Python dependencies for setup
└── README.md
```

## Strategies

### Threshold Strategy
Buys when price drops below a threshold and sells when it rises above another threshold.

```yaml
strategy:
  mode: "threshold"
  buy_below: 0.45
  sell_above: 0.55
```

### Market Making (Placeholder)
Provides liquidity by quoting both bid and ask.

```yaml
strategy:
  mode: "market_making"
```

### Momentum (Placeholder)
Follows price momentum trends.

```yaml
strategy:
  mode: "momentum"
```

## Security Best Practices

1. **Never commit credentials** - `config.yml` is in `.gitignore` by default
2. **Keep private keys secure** - Never share or commit your wallet private key
3. **Use vault for production** - Enable `security.use_vault` for production deployments
4. **Validate SSL certificates** - Don't disable TLS verification
5. **Monitor API keys** - Rotate regularly
6. **Test with small amounts** - Start with minimal position sizes
7. **Backup credentials safely** - Store `config.yml` backups in secure, encrypted storage

## Development

### Running Tests

```bash
go test ./...
```

### Building for Production

```bash
# Linux AMD64
GOOS=linux GOARCH=amd64 go build -o polymarket-bot-linux ./cmd/bot

# Optimized build
go build -ldflags="-s -w" -o polymarket-bot ./cmd/bot
```

### Adding a New Strategy

1. Implement strategy logic in `internal/strategy/engine.go`
2. Add mode to config: `strategy.mode: "your_strategy"`
3. Update strategy switch case in `ProcessMarketData()`

## Performance Tips

1. **Deploy close to exchange** - Minimize network latency
2. **Use dedicated servers** - Avoid shared hosting
3. **Tune buffer sizes** - Adjust `websocket.message_buffer_size`
4. **Enable batching** - Set `orders.batch_size > 1` if API supports it
5. **Monitor GC pressure** - Adjust `performance.gc_percent`
6. **Profile bottlenecks** - Use Go's pprof

## Troubleshooting

### WebSocket disconnects frequently
- Check network stability
- Increase `websocket.pong_timeout_sec`
- Verify firewall isn't blocking persistent connections

### High latency
- Deploy closer to exchange
- Reduce `websocket.message_buffer_size` if overflow
- Check CPU usage and increase `performance.worker_count`

### Orders rejected
- Verify API credentials
- Check rate limits: `orders.max_orders_per_second`
- Ensure sufficient account balance

### Auto-halt triggered
- Review `risk.max_daily_loss` and `monitoring.max_error_rate_pct`
- Check metrics at `/metrics` endpoint
- Review logs for recurring errors

## License

MIT License - See LICENSE file for details

## Disclaimer

This software is for educational purposes. Trading cryptocurrencies and prediction markets carries risk. Always test thoroughly and start with small amounts. The authors are not responsible for financial losses.