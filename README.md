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
