package api

import (
	"strings"
	"testing"
	"time"
)

// Reference values were produced with eth_account (encode_typed_data +
// sign_message) using the same typed data the official clob-client-v2 SDK
// signs. The key is the well-known Hardhat test account #0.
const testKey = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

const testToken = "71321045679252212594626385532706912750332728571942532289631379312455583992563"

func testBuilder(t *testing.T, maker string, sigType int) *OrderBuilder {
	t.Helper()
	s, err := NewSigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	var m Address
	if maker != "" {
		m = mustAddress(maker)
	}
	b, err := NewOrderBuilder(s, m, sigType)
	if err != nil {
		t.Fatal(err)
	}
	b.now = func() time.Time { return time.UnixMilli(1760000000000) }
	b.salt = func() int64 { return 479249096354 }
	return b
}

func TestSignerAddress(t *testing.T) {
	s, _ := NewSigner(testKey)
	if got := s.Address().String(); got != "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266" {
		t.Errorf("address = %s", got)
	}
}

func TestOrderSignatures(t *testing.T) {
	// BUY 10 @ 0.52 => makerAmount 5.2 pUSD, takerAmount 10 shares (the docs' example)
	buy := OrderParams{TokenID: testToken, Side: "buy", Price: 0.52, Size: 10, TickSize: "0.01", Version: 2}
	// SELL 10 @ 0.52 swaps the amounts
	sell := buy
	sell.Side, sell.NegRisk = "sell", true
	v3 := buy
	v3.Version = 3

	tests := []struct {
		name    string
		maker   string
		sigType int
		params  OrderParams
		want    string
	}{
		{"EOA v2 buy", "", SigTypeEOA, buy,
			"0x86194dddfb51e77fa329ea292aca114bf8faa4887534b4b28bb635bb738b95644508928fdb51ddd5cb113358fbaeb732f787f1a93c686e311483da7dc813d9b01c"},
		{"EOA v2 neg-risk sell", "", SigTypeEOA, sell,
			"0xc0d1e7e50ae3dbb8705b7ac770b50f4d775d58cc9718f12b76e81b3785cf2180373f077641533523d856803e33560d0546bcf13ef31789b9cac0c43d221b91b01b"},
		{"EOA v3 buy", "", SigTypeEOA, v3,
			"0x503d12b8e6293badeba8fb79c5e68c0c40b7e80ab886e32998e5d1524a7d36512c3bbb0fa2da485701da23caca109e7e23fc0c28fec6f85a15c293fcf4d806b21b"},
		{"proxy wallet v2 buy", "0x1111111111111111111111111111111111111111", SigTypePolyProxy, buy,
			"0xe42923aba1fed6e8f7f517776e49f1f1e007f188e14d5a47ba437d45129e3f2142f204f96c991fff874e229fe9b741ef3fa6cf075cdff587aed3fc253aea323e1c"},
		{"deposit wallet (1271) v2 buy", "0x2222222222222222222222222222222222222222", SigTypePoly1271, buy,
			"0xfb72907f3227b2346dd2320fa33561f0d73005f40e7d6ba1d36e3d6d055216572f1c6b37507848acaca63bbc34954b09b3ad2efce0cc1f2d0efa489415d094911b3264e159346253e26a64e00b69032db0e7d32f94628de3e6eecb50304d7af3d28455ff1cd8b42d4e3583d1a3edabe8f56e8c910043fb4a1a717a3ae62e3dafd94f726465722875696e743235362073616c742c61646472657373206d616b65722c61646472657373207369676e65722c75696e7432353620746f6b656e49642c75696e74323536206d616b6572416d6f756e742c75696e743235362074616b6572416d6f756e742c75696e743820736964652c75696e7438207369676e6174757265547970652c75696e743235362074696d657374616d702c62797465733332206d657461646174612c62797465733332206275696c6465722900ba"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := testBuilder(t, tt.maker, tt.sigType).Build(tt.params)
			if err != nil {
				t.Fatal(err)
			}
			if o.Signature != tt.want {
				t.Errorf("signature\n got %s\nwant %s", o.Signature, tt.want)
			}
		})
	}
}

func TestOrderFields(t *testing.T) {
	b := testBuilder(t, "0x2222222222222222222222222222222222222222", SigTypePoly1271)
	o, err := b.Build(OrderParams{TokenID: testToken, Side: "sell", Price: 0.5249, Size: 10, TickSize: "0.01", Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	// price rounds to the tick (0.52); sell puts shares on the maker side
	if o.MakerAmount != "10000000" || o.TakerAmount != "5200000" || o.Side != "SELL" {
		t.Errorf("amounts/side = %s %s %s", o.MakerAmount, o.TakerAmount, o.Side)
	}
	// for deposit wallets the wallet itself is the signer of record
	if o.Signer != o.Maker || o.SignatureType != 3 || o.Timestamp != "1760000000000" || o.Expiration != "0" {
		t.Errorf("unexpected fields %+v", o)
	}
}

func TestOrderValidation(t *testing.T) {
	b := testBuilder(t, "", SigTypeEOA)
	base := OrderParams{TokenID: testToken, Side: "buy", Price: 0.5, Size: 1, TickSize: "0.01", Version: 2}
	bad := map[string]func(p *OrderParams){
		"price below tick":   func(p *OrderParams) { p.Price = 0.004 },
		"price above 1-tick": func(p *OrderParams) { p.Price = 0.996 },
		"unknown tick":       func(p *OrderParams) { p.TickSize = "0.02" },
		"bad token":          func(p *OrderParams) { p.TokenID = "abc" },
		"bad side":           func(p *OrderParams) { p.Side = "hold" },
		"v1 exchange":        func(p *OrderParams) { p.Version = 1 },
		"zero size":          func(p *OrderParams) { p.Size = 0 },
	}
	for name, mutate := range bad {
		p := base
		mutate(&p)
		if _, err := b.Build(p); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := NewOrderBuilder(b.signer, Address{}, SigTypeGnosisSafe); err == nil {
		t.Error("safe wallet without funder address accepted")
	}
}

func TestL2Signature(t *testing.T) {
	// reference: base64.urlsafe_b64encode(hmac.new(urlsafe_b64decode(secret), msg, sha256))
	got, err := l2Signature("c3VwZXItc2VjcmV0LWtleS1ieXRlcy0wMTIzNDU2Nzg5", 1760000000, "POST", "/order", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "vjrJAC9Blhc4C68DK3nfOQI9kq8fx92nXqWQQZA0AmA=" {
		t.Errorf("signature = %s", got)
	}
	if _, err := l2Signature("not base64!!", 1, "GET", "/", nil); err == nil {
		t.Error("invalid secret accepted")
	}
}

func TestSaltIsSafeJSONInteger(t *testing.T) {
	for i := 0; i < 100; i++ {
		if s := randomSalt(); s < 0 || s > 1<<53 {
			t.Fatalf("salt %d out of range", s)
		}
	}
}

func TestAddressChecksum(t *testing.T) {
	a := mustAddress(strings.ToLower("0xE111180000d2663C0091e4f400237545B87B996B"))
	if a.String() != "0xE111180000d2663C0091e4f400237545B87B996B" {
		t.Errorf("checksum = %s", a)
	}
}
