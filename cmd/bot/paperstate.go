package main

import (
	"context"
	"time"

	"github.com/seeker/polymarket-bot/internal/order"
	"github.com/seeker/polymarket-bot/internal/paper"
	"github.com/seeker/polymarket-bot/internal/strategy"
)

const stateSaveInterval = 5 * time.Second

// loadPaperTrader restores the paper portfolio saved at path, or starts a new
// one if there is none (or reset is set). A file that exists but can't be read
// is fatal rather than silently replaced, so a bad file never wipes history.
func loadPaperTrader(path string, startingBalance float64, reset bool) *paper.Trader {
	if reset {
		logState.Infof("Starting a fresh paper portfolio (-paper-reset); %s will be overwritten", path)
		return paper.NewTrader(startingBalance)
	}

	state, err := paper.LoadState(path)
	if err != nil {
		logState.Fatalf("%v (fix or delete the file, or run with -paper-reset)", err)
	}
	if state == nil {
		logState.Infof("No saved paper portfolio at %s, starting with $%.2f", path, startingBalance)
		return paper.NewTrader(startingBalance)
	}

	trader, err := paper.Restore(state)
	if err != nil {
		logState.Fatalf("Can't restore %s: %v (fix or delete the file, or run with -paper-reset)", path, err)
	}
	logState.Infof("Restored paper portfolio from %s (saved %s): %d positions, %d trades",
		path, state.SavedAt.Local().Format("2006-01-02 15:04:05"), len(state.Positions), len(state.Trades))
	if state.StartingBalance != startingBalance {
		logState.Warnf("Saved starting balance $%.2f differs from config $%.2f; keeping the saved portfolio (use -paper-reset to start over)",
			state.StartingBalance, startingBalance)
	}
	return trader
}

// syncRestoredPaperState makes the strategy and risk manager aware of a
// restored portfolio: open positions, their exposure, and today's realized
// losses for the daily loss limit.
func syncRestoredPaperState(trader *paper.Trader, strategyEngine *strategy.Engine, orderManager *order.Manager) {
	for _, p := range trader.Summary().Positions {
		strategyEngine.UpdatePosition(p.MarketID, p.Side, p.Quantity, p.AvgPrice)
		orderManager.SetMarketExposure(p.MarketID, p.Exposure)
	}

	y, m, d := time.Now().Date()
	for _, tr := range trader.Trades() {
		if ty, tm, td := tr.ExecutedAt.Local().Date(); ty == y && tm == m && td == d {
			orderManager.RecordTrade(tr)
		}
	}
}

// paperSaveLoop saves the portfolio whenever it has changed, checking every
// few seconds, so a crash loses at most that much. It closes done on exit.
func paperSaveLoop(ctx context.Context, trader *paper.Trader, path string, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(stateSaveInterval)
	defer ticker.Stop()

	saved := trader.Changes()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c := trader.Changes(); c != saved && savePaperState(trader, path) {
				saved = c
			}
		}
	}
}

// savePaperState writes the portfolio to path, logging any failure.
func savePaperState(trader *paper.Trader, path string) bool {
	if err := paper.SaveState(path, trader.Snapshot()); err != nil {
		logState.Errorf("%v", err)
		return false
	}
	logState.Debugf("Saved paper portfolio to %s", path)
	return true
}
