package paper

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/seeker/polymarket-bot/pkg/types"
)

// stateVersion is bumped if the file layout changes incompatibly.
const stateVersion = 1

// State is everything needed to resume a paper portfolio after a restart.
type State struct {
	Version         int                `json:"version"`
	SavedAt         time.Time          `json:"saved_at"`
	StartingBalance float64            `json:"starting_balance"`
	Cash            float64            `json:"cash"`
	RealizedPnL     float64            `json:"realized_pnl"`
	Positions       map[string]Holding `json:"positions"`   // net position per market
	LastPrices      map[string]float64 `json:"last_prices"` // last mark per market
	Trades          []*types.Trade     `json:"trades"`
	NextID          int                `json:"next_id"`
}

// Holding is a saved net position: Qty > 0 is long, Qty < 0 is short.
type Holding struct {
	Qty      int     `json:"qty"`
	AvgPrice float64 `json:"avg_price"`
}

// Snapshot returns a copy of the trader's state for saving.
func (t *Trader) Snapshot() *State {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := &State{
		Version:         stateVersion,
		SavedAt:         time.Now(),
		StartingBalance: t.startingBal,
		Cash:            t.cash,
		RealizedPnL:     t.realizedPnL,
		Positions:       make(map[string]Holding, len(t.positions)),
		LastPrices:      make(map[string]float64, len(t.lastPrices)),
		Trades:          make([]*types.Trade, len(t.trades)),
		NextID:          t.nextID,
	}
	for id, h := range t.positions {
		s.Positions[id] = Holding{Qty: h.Qty, AvgPrice: h.AvgPrice}
	}
	for id, p := range t.lastPrices {
		s.LastPrices[id] = p
	}
	for i, tr := range t.trades {
		c := *tr
		s.Trades[i] = &c
	}
	return s
}

// Restore builds a trader from saved state.
func Restore(s *State) (*Trader, error) {
	if s.Version != stateVersion {
		return nil, fmt.Errorf("paper: unsupported state version %d (want %d)", s.Version, stateVersion)
	}
	if s.StartingBalance <= 0 || math.IsNaN(s.Cash) || s.Cash < -1e-9 {
		return nil, fmt.Errorf("paper: invalid saved balances (starting %.2f, cash %.2f)", s.StartingBalance, s.Cash)
	}

	t := NewTrader(s.StartingBalance)
	t.cash = s.Cash
	t.realizedPnL = s.RealizedPnL
	t.nextID = s.NextID
	for id, h := range s.Positions {
		if h.Qty == 0 {
			continue
		}
		if h.AvgPrice <= 0 || h.AvgPrice >= 1 {
			return nil, fmt.Errorf("paper: invalid saved price %.4f for %s", h.AvgPrice, id)
		}
		t.positions[id] = &holding{Qty: h.Qty, AvgPrice: h.AvgPrice}
	}
	for id, p := range s.LastPrices {
		t.lastPrices[id] = p
	}
	for _, tr := range s.Trades {
		if tr != nil {
			c := *tr
			t.trades = append(t.trades, &c)
		}
	}
	return t, nil
}

// SaveState writes s to path as JSON, creating the directory if needed. It
// writes to a temporary file first and renames it, so a crash mid-write
// never leaves a truncated state file behind.
func SaveState(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("paper: create state dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("paper: encode state: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("paper: save state: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("paper: save state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("paper: save state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("paper: save state: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("paper: save state: %w", err)
	}
	return nil
}

// LoadState reads state saved by SaveState. It returns nil, nil if the file
// does not exist yet.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("paper: load state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("paper: parse %s: %w", path, err)
	}
	return &s, nil
}

// Changes returns a counter that increases with every fill, so callers can
// tell whether the portfolio changed since they last saved it.
func (t *Trader) Changes() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return uint64(t.nextID)
}
