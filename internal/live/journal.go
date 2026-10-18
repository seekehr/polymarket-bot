package live

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Fill is one of our executions within a trade. A trade can contain several
// of our fills when it matched more than one of our orders.
type Fill struct {
	Key         string    `json:"key"` // trade ID + our order ID; unique per fill
	TradeID     string    `json:"trade_id"`
	OrderID     string    `json:"order_id"`
	AssetID     string    `json:"asset_id"`
	Market      string    `json:"market"` // condition ID
	Outcome     string    `json:"outcome,omitempty"`
	Side        string    `json:"side"` // "buy" or "sell"
	Price       float64   `json:"price"`
	Size        float64   `json:"size"`
	Role        string    `json:"role"`   // TAKER or MAKER
	Status      string    `json:"status"` // MATCHED, MINED, CONFIRMED, RETRYING, FAILED
	FeeRateBps  string    `json:"fee_rate_bps,omitempty"`
	RealizedPnL float64   `json:"realized_pnl"` // from our average cost, excluding fees
	MatchedAt   time.Time `json:"matched_at"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// Journal is an append-only JSON-lines record of live fills. A fill is
// written again whenever its settlement status changes; the last line for a
// key wins when the file is read back.
type Journal struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// OpenJournal opens (creating if needed) the journal at path.
func OpenJournal(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	return &Journal{path: path, f: f}, nil
}

// Append writes one fill record and flushes it to disk.
func (j *Journal) Append(f Fill) error {
	f.RecordedAt = time.Now()
	line, err := json.Marshal(f)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	return j.f.Sync()
}

// Close closes the journal file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

// ReadJournal returns the latest record for each fill, oldest first. A
// missing file yields no fills; an unreadable line (say, from a crash
// mid-write) is skipped.
func ReadJournal(path string) ([]Fill, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	defer f.Close()

	latest := make(map[string]int)
	var fills []Fill
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var fl Fill
		if json.Unmarshal(sc.Bytes(), &fl) != nil || fl.Key == "" {
			continue
		}
		if i, ok := latest[fl.Key]; ok {
			fills[i] = fl
			continue
		}
		latest[fl.Key] = len(fills)
		fills = append(fills, fl)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	return fills, nil
}

// Path returns the journal's file path.
func (j *Journal) Path() string { return j.path }
