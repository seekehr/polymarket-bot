package paper

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seeker/polymarket-bot/pkg/types"
)

func TestSaveLoadRestoreRoundTrip(t *testing.T) {
	tr := NewTrader(100)
	if _, err := tr.Execute(order("buy", 0.40, 50)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Execute(&types.OrderRequest{MarketID: "m2", Side: "sell", Price: 0.70, Quantity: 10}); err != nil {
		t.Fatal(err)
	}
	tr.Mark("m1", 0.45)

	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := SaveState(path, tr.Snapshot()); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(loaded)
	if err != nil {
		t.Fatal(err)
	}

	want, got := tr.Summary(), restored.Summary()
	approx(t, "cash", got.Cash, want.Cash)
	approx(t, "equity", got.Equity, want.Equity)
	approx(t, "unrealized", got.UnrealizedPnL, want.UnrealizedPnL)
	if got.Trades != 2 || len(got.Positions) != 2 {
		t.Fatalf("restored %d trades, %d positions; want 2, 2", got.Trades, len(got.Positions))
	}
	if got.Positions[1].Side != "short" || got.Positions[1].Quantity != 10 {
		t.Errorf("short position not restored: %+v", got.Positions[1])
	}
	approx(t, "short exposure", got.Positions[1].Exposure, 0.30*10)

	// New fills continue the ID sequence instead of reusing old IDs.
	fill, err := restored.Execute(order("sell", 0.50, 50))
	if err != nil {
		t.Fatal(err)
	}
	if fill.Trade.ID != "paper-trade-3" {
		t.Errorf("next trade ID = %s, want paper-trade-3", fill.Trade.ID)
	}

	// Snapshots are copies: changing the trader doesn't alter a taken snapshot.
	snap := restored.Snapshot()
	restored.Execute(order("buy", 0.40, 1))
	if len(snap.Trades) != 3 {
		t.Errorf("snapshot changed after a later fill: %d trades", len(snap.Trades))
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "nope.json"))
	if s != nil || err != nil {
		t.Errorf("LoadState(missing) = %v, %v; want nil, nil", s, err)
	}
}

func TestLoadStateCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	if _, err := LoadState(path); err == nil {
		t.Error("LoadState accepted a corrupt file")
	}
}

func TestRestoreRejectsBadState(t *testing.T) {
	good := NewTrader(100).Snapshot()

	bad := *good
	bad.Version = 99
	if _, err := Restore(&bad); err == nil {
		t.Error("Restore accepted an unknown version")
	}

	bad = *good
	bad.Positions = map[string]Holding{"m1": {Qty: 5, AvgPrice: 1.5}}
	if _, err := Restore(&bad); err == nil {
		t.Error("Restore accepted an out-of-range price")
	}
}

func TestSaveStateLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for i := 0; i < 2; i++ { // second save replaces the first
		if err := SaveState(path, NewTrader(100).Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want just state.json", len(entries))
	}
}
