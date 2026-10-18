package main

import "github.com/seeker/polymarket-bot/internal/logging"

// Log topics: the label at the start of each line says what the bot is doing.
var (
	logSystem = logging.NewTopic("SYSTEM", logging.Blue)    // startup, shutdown, runtime tuning
	logConfig = logging.NewTopic("CONFIG", logging.Blue)    // loading config.yml
	logAPI    = logging.NewTopic("API", logging.Cyan)       // REST API connectivity
	logWS     = logging.NewTopic("WS", logging.Cyan)        // websocket feed
	logSignal = logging.NewTopic("SIGNAL", logging.Magenta) // trade signals from the strategy
	logStrat  = logging.NewTopic("STRAT", logging.Magenta)  // strategy failures
	logOrder  = logging.NewTopic("ORDER", logging.Green)    // order submission
	logFill   = logging.NewTopic("FILL", logging.Green)     // paper fills
	logPnL    = logging.NewTopic("PNL", logging.Green)      // portfolio and positions
	logPaper  = logging.NewTopic("PAPER", logging.Magenta)  // paper-mode banner
	logLive   = logging.NewTopic("LIVE", logging.Red)       // live-mode warning
	logAlert  = logging.NewTopic("ALERT", logging.Yellow)   // metric threshold alerts
	logStats  = logging.NewTopic("STATS", logging.Blue)     // runtime and final statistics
)
