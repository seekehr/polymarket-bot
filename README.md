# polymarket-bot

A trading bot for Polymarket, written in Go. It listens to the CLOB websocket for
the markets you pick, runs a simple strategy on each price update, and places
orders through the REST API.

By default it runs in paper mode: real prices, fake money, no orders sent. Use
that until you trust it.

## Setup

You need Go 1.25+. Python 3 is only needed if you want the credentials script.

```bash
git clone https://github.com/seekehr/polymarket-bot
cd polymarket-bot
go mod download
cp config.example.yml config.yml
```

Then open `config.yml` and set `markets.target_market_ids` to the markets you
want to watch. These are the CLOB token IDs (the long numbers), not the IDs
from the polymarket.com URL.

If you're going to trade for real, you also need API credentials. Put your
wallet's private key (with or without `0x`) in `config.yml` under `api.private_key`, then run:

```bash
python -m venv .venv
source .venv/bin/activate    # Windows: .venv\Scripts\Activate.ps1
pip install -r requirements.txt
python scripts/setup_credentials.py
```

It derives your API keys from the private key and writes `api_key`,
`api_secret` and `passphrase` into `config.yml`. You can skip this for paper
trading.

## Running

```bash
go run ./cmd/bot -config config.yml
```

Or build it first with `go build -o polymarket-bot ./cmd/bot`. Ctrl+C stops it
and prints a summary.

Pass `-paper` to force paper mode no matter what the config says.

## Paper trading

```yaml
paper_trading:
  enabled: true
  starting_balance: 1000
  report_interval_sec: 60
```

Orders fill instantly at the signal price against the virtual balance, and the
portfolio gets logged every `report_interval_sec`. No slippage, fees or partial
fills are modelled, so real results will be worse than what you see here.

## Live trading

Only do this with money you can afford to lose. You'll need USDC on Polygon in
your wallet and a little POL for gas.

1. Set up your private key and API credentials (see Setup above). You can also
   use `${ENV_VAR}` syntax in `config.yml` to keep the key out of the file.
2. Set `paper_trading.enabled: false`.

`config.yml` is gitignored. Keep it that way.

## Config

Everything lives in `config.yml`; the example file has comments for each option.
The main things to tune:

- `strategy.mode`: `threshold` (buy below `buy_below`, sell above `sell_above`)
  or `market_making`. `momentum` isn't implemented yet.
- `strategy.quantity` / `max_position_size`: contracts per order and in total
- `risk`: daily loss cap, per-market exposure limit
- `orders`: rate limits, batching, retries

Prometheus metrics are served on `monitoring.metrics_port` (9090 by default).

## Known issues

- In live mode, filled orders don't update the strategy's position tracking, so
  the bot never sees that it's holding anything and won't exit trades. Paper
  mode doesn't have this problem.
- Tests only cover the paper trader so far (`go test ./...`).
