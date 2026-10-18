package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// MarketToken is one tradable outcome token, resolved from a market reference.
type MarketToken struct {
	TokenID  string
	Outcome  string // e.g. "Yes", "Bills", "Over"
	Question string // the market's title
	Slug     string
}

// IsMarketURL reports whether a target_market_ids entry is a polymarket.com
// link rather than a token ID.
func IsMarketURL(ref string) bool {
	r := strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(r, "http://") || strings.HasPrefix(r, "https://") || strings.HasPrefix(r, "polymarket.com/")
}

// gammaMarket is the part of a Gamma API market the bot needs. outcomes and
// clobTokenIds arrive as JSON-encoded strings.
type gammaMarket struct {
	Slug            string `json:"slug"`
	Question        string `json:"question"`
	Outcomes        string `json:"outcomes"`
	ClobTokenIDs    string `json:"clobTokenIds"`
	Closed          bool   `json:"closed"`
	AcceptingOrders bool   `json:"acceptingOrders"`
	EnableOrderBook bool   `json:"enableOrderBook"`
}

// ResolveMarketURL turns a polymarket.com link into its outcome tokens.
//
// The last path segment is looked up as a market slug first (that covers
// /event/<event>/<market> links, and event pages whose main market shares the
// event's slug, like sports games), then as an event slug, which works when
// the event has a single open market. Add "#Outcome" to the link to keep just
// one side, e.g. ".../nfl-buf-lv-2026-10-18#Bills".
func (c *Client) ResolveMarketURL(ref string) ([]MarketToken, error) {
	raw := strings.TrimSpace(ref)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), "polymarket.com") {
		return nil, fmt.Errorf("%q is not a polymarket.com link", ref)
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return nil, fmt.Errorf("%q has no market in its path", ref)
	}
	slug := parts[len(parts)-1]
	outcome := strings.TrimSpace(u.Fragment)

	m, err := c.findMarket(slug)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	if m.Closed || !m.AcceptingOrders || !m.EnableOrderBook {
		return nil, fmt.Errorf("%s: market %q is closed or not taking orders", ref, m.Question)
	}

	var outcomes, tokens []string
	if json.Unmarshal([]byte(m.Outcomes), &outcomes) != nil || json.Unmarshal([]byte(m.ClobTokenIDs), &tokens) != nil ||
		len(tokens) == 0 || len(outcomes) != len(tokens) {
		return nil, fmt.Errorf("%s: market %q has no order book tokens", ref, m.Question)
	}

	var out []MarketToken
	for i, id := range tokens {
		if outcome == "" || strings.EqualFold(outcome, outcomes[i]) {
			out = append(out, MarketToken{TokenID: id, Outcome: outcomes[i], Question: m.Question, Slug: m.Slug})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: market %q has no outcome %q (it has %s)", ref, m.Question, outcome, strings.Join(outcomes, ", "))
	}
	return out, nil
}

// findMarket looks a slug up as a market, then as a single-market event.
func (c *Client) findMarket(slug string) (*gammaMarket, error) {
	var markets []gammaMarket
	if err := c.publicGet(c.gammaURL, "/markets", url.Values{"slug": {slug}}, &markets); err != nil {
		return nil, fmt.Errorf("market lookup failed: %w", err)
	}
	if len(markets) > 0 {
		return &markets[0], nil
	}

	var events []struct {
		Title   string        `json:"title"`
		Markets []gammaMarket `json:"markets"`
	}
	if err := c.publicGet(c.gammaURL, "/events", url.Values{"slug": {slug}}, &events); err != nil {
		return nil, fmt.Errorf("event lookup failed: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("no market or event with slug %q", slug)
	}

	var open []gammaMarket
	for _, m := range events[0].Markets {
		if !m.Closed && m.AcceptingOrders {
			open = append(open, m)
		}
	}
	switch len(open) {
	case 0:
		return nil, fmt.Errorf("event %q has no open markets", events[0].Title)
	case 1:
		return &open[0], nil
	}
	examples := make([]string, 0, 5)
	for _, m := range open[:min(5, len(open))] {
		examples = append(examples, fmt.Sprintf("polymarket.com/event/%s/%s (%s)", slug, m.Slug, m.Question))
	}
	return nil, fmt.Errorf("event %q has %d open markets; link to one of them instead, e.g.\n  %s",
		events[0].Title, len(open), strings.Join(examples, "\n  "))
}
