package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seeker/polymarket-bot/internal/config"
)

func gammaClient(t *testing.T) *Client {
	t.Helper()
	market := func(slug, q, outcomes, ids string, open bool) string {
		closed := "false"
		if !open {
			closed = "true"
		}
		return `{"slug":"` + slug + `","question":"` + q + `","outcomes":"` + outcomes + `","clobTokenIds":"` + ids +
			`","closed":` + closed + `,"acceptingOrders":` + map[bool]string{true: "true", false: "false"}[open] + `,"enableOrderBook":true}`
	}
	game := market("nfl-buf-lv", "Bills vs. Raiders", `[\"Bills\", \"Raiders\"]`, `[\"111\", \"222\"]`, true)
	spread := market("nfl-buf-lv-spread", "Spread: Bills (-3.5)", `[\"Bills\", \"Raiders\"]`, `[\"333\", \"444\"]`, true)
	single := market("will-it-rain", "Will it rain?", `[\"Yes\", \"No\"]`, `[\"555\", \"666\"]`, true)
	closed := market("old-game", "Old game", `[\"Yes\", \"No\"]`, `[\"777\", \"888\"]`, false)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := r.URL.Query().Get("slug")
		switch r.URL.Path {
		case "/markets":
			switch slug {
			case "nfl-buf-lv":
				io.WriteString(w, "["+game+"]")
			case "nfl-buf-lv-spread":
				io.WriteString(w, "["+spread+"]")
			case "old-game":
				io.WriteString(w, "["+closed+"]")
			default:
				io.WriteString(w, "[]")
			}
		case "/events":
			switch slug {
			case "rain-event":
				io.WriteString(w, `[{"title":"Rain","markets":[`+single+`,`+closed+`]}]`)
			case "big-event":
				io.WriteString(w, `[{"title":"Big","markets":[`+game+`,`+spread+`]}]`)
			default:
				io.WriteString(w, "[]")
			}
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient(&config.Config{API: config.APIConfig{GammaAPIURL: srv.URL, TimeoutMS: 2000}})
}

func ids(tokens []MarketToken) string {
	var out []string
	for _, t := range tokens {
		out = append(out, t.Outcome+"="+t.TokenID)
	}
	return strings.Join(out, ",")
}

func TestResolveMarketURL(t *testing.T) {
	c := gammaClient(t)
	ok := map[string]string{
		"https://polymarket.com/sports/nfl/nfl-buf-lv":         "Bills=111,Raiders=222", // event page sharing its main market's slug
		"https://polymarket.com/sports/nfl/nfl-buf-lv#raiders": "Raiders=222",           // one outcome, any case
		"polymarket.com/event/big-event/nfl-buf-lv-spread":     "Bills=333,Raiders=444", // explicit market in an event
		"https://polymarket.com/event/rain-event?tid=123#Yes":  "Yes=555",               // single open market event
		"https://www.polymarket.com/event/rain-event/":         "Yes=555,No=666",
	}
	for ref, want := range ok {
		got, err := c.ResolveMarketURL(ref)
		if err != nil {
			t.Errorf("%s: %v", ref, err)
			continue
		}
		if ids(got) != want {
			t.Errorf("%s = %s, want %s", ref, ids(got), want)
		}
	}

	bad := map[string]string{
		"https://polymarket.com/event/big-event":       "2 open markets",
		"https://polymarket.com/event/old-game":        "closed",
		"https://polymarket.com/event/nfl-buf-lv#Ties": "no outcome",
		"https://polymarket.com/event/nope":            "no market or event",
		"https://example.com/event/nfl-buf-lv":         "not a polymarket.com link",
	}
	for ref, want := range bad {
		if _, err := c.ResolveMarketURL(ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", ref, err, want)
		}
	}
}

func TestIsMarketURL(t *testing.T) {
	for ref, want := range map[string]bool{
		"https://polymarket.com/event/x": true, "polymarket.com/event/x": true, "HTTP://polymarket.com/x": true,
		"32338220190071351435772801779725302244575775216413325951443816017994629993401": false,
	} {
		if IsMarketURL(ref) != want {
			t.Errorf("IsMarketURL(%q) = %v", ref, !want)
		}
	}
}
