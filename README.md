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
want to trade. Each entry is either a polymarket.com link or a CLOB token ID:

```yaml
markets:
  target_market_ids:
    - "https://polymarket.com/sports/nfl/nfl-buf-lv-2026-10-18#Bills"
    - "https://polymarket.com/event/xi-jinping-out-before-2027#Yes"
    - "32338220190071351435772801779725302244575775216413325951443816017994629993401"
```

Links are turned into token IDs at startup, and the bot logs what each one
resolved to. Every market has one token per outcome (Yes/No, or the two teams,
Over/Under, ...), and their prices add up to about $1. The strategy trades
each token you list on its own: it buys when the price drops below
`buy_below` and sells above `sell_above`.

- Add `#Outcome` to a link to trade one side, e.g. `#Bills` or `#Yes`. Without
  it you get every outcome of the market. On a two-sided market that usually
  means betting both ways at once, so pick one.
- An event page with several markets (a game's spreads and totals, say)
  resolves to the market that shares the page's slug, which for games is the
  moneyline. For another one, open it on polymarket.com and use its own link
  (`polymarket.com/event/<event>/<market>`). If the bot can't tell which
  market you mean, it stops and lists some.
- Closed markets, and anything Polymarket doesn't know, stop the bot at startup.

To find token IDs yourself, take the slug from the market's URL and run:

```bash
curl "https://gamma-api.polymarket.com/events?slug=<slug>"
```

Each market in the response has `outcomes` and `clobTokenIds` in the same order.

If you're going to trade for real, you also need API credentials. Put your
wallet's private key (with or without `0x`) in `config.yml` under `api.private_key`, then run:

```bash
python -m venv .venv
source .venv/bin/activate    # Windows: .venv\Scripts\Activate.ps1
pip install -r requirements.txt
python scripts/setup_credentials.py
```

It derives your API keys from the private key and writes `api_key`,
`api_secret` and `passphrase` into `config.yml`. It also works out which wallet
holds your funds and sets `api.signature_type` and `api.funder_address` for you:
every Polymarket wallet type (proxy, Safe, deposit wallet, or the key's own
address) has an address derived from your key, so the script computes each
one and picks the one with activity on Polymarket. If it can't tell (a brand-new
account, or more than one wallet in use), copy your wallet address from the
profile menu on polymarket.com and pass it in:

```bash
python scripts/setup_credentials.py --wallet 0xYourWalletAddress
```

It refuses a wallet your key doesn't own. The private key has to be the
account owner's: deposit wallet session keys aren't supported. You can skip
all of this for paper trading.

The script rewrites `config.yml` with PyYAML, so comments in it are dropped.

## Running

```bash
go run ./cmd/bot -config config.yml
```

Or build it first with `go build -o polymarket-bot ./cmd/bot`. Ctrl+C stops it
and prints a summary.

Pass `-paper` to force paper mode no matter what the config says.

Log levels are colored when logging to a terminal. Use `-color=always` or
`-color=never` to override, or set `NO_COLOR=1` to turn colors off.
`monitoring.log_level` in the config sets the minimum level shown.

## Paper trading

```yaml
paper_trading:
  enabled: true
  starting_balance: 1000
  report_interval_sec: 60
  state_file: data/paper_state.json
```

Orders fill instantly at the signal price against the virtual balance, and the
portfolio gets logged every `report_interval_sec`. The portfolio (cash, positions,
trade history) is saved to `data/paper_state.json` (set `state_file` to change
it) a few seconds after each fill and on shutdown, and restored on the next
start. Run with `-paper-reset` to start over from `starting_balance`. No slippage, fees or partial
fills are modelled, so real results will be worse than what you see here.

## Live trading

Only do this with money you can afford to lose.

1. Set up your private key and API credentials (see Setup above). You can also
   use `${ENV_VAR}` syntax in `config.yml` to keep the key out of the file.
2. Check `api.signature_type` and `api.funder_address`, which the script fills
   in (see the comments in `config.example.yml` to set them by hand).
3. Make sure that wallet has pUSD (Polymarket's collateral since the April 2026
   V2 upgrade) and the exchange approvals in place. Trading once on
   polymarket.com with the same wallet sets this up; the bot doesn't do it.
4. Set `paper_trading.enabled: false`.

On startup the bot checks every target market is tradable, loads your real
positions and resting orders from Polymarket, and connects to your account
feed. If any of that fails it refuses to start. While running:

- Orders are signed locally (EIP-712, Polymarket CLOB V2) and posted to the CLOB.
- Fills and order updates arrive over the account websocket and update the
  positions the strategy and risk limits see. After a reconnect, missed fills
  are fetched over REST.
- Every fill is appended to `data/live_trades.jsonl`, with a new line each
  time its settlement status changes. Today's realized losses are read back
  from it on restart, so `max_daily_loss` survives restarts.
- Polymarket has no shorting: sells are capped at the shares you hold, and a
  "sell" signal with nothing held is skipped.
- Only one order per market and side is kept working at a time.

Orders still resting when you stop the bot stay on the book and can fill.
P&L figures exclude fees.

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
