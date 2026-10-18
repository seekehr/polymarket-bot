package api

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Polymarket CLOB V2 order signing (EIP-712), matching the official
// clob-client-v2 SDK. Only Polygon mainnet is supported.

const (
	polygonChainID = 137

	exchangeDomainName = "Polymarket CTF Exchange"

	orderTypeString  = "Order(uint256 salt,address maker,address signer,uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint8 side,uint8 signatureType,uint256 timestamp,bytes32 metadata,bytes32 builder)"
	domainTypeString = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
	// ERC-7739 wrapper used for POLY_1271 (deposit wallet / smart contract) signatures
	typedDataSignTypeString = "TypedDataSign(Order contents,string name,string version,uint256 chainId,address verifyingContract,bytes32 salt)" + orderTypeString
)

// Exchange contracts on Polygon.
var (
	exchangeV2        = mustAddress("0xE111180000d2663C0091e4f400237545B87B996B")
	negRiskExchangeV2 = mustAddress("0xe2222d279d744050d28e00520010520000310F59")
	exchangeV3        = mustAddress("0xe3333700cA9d93003F00f0F71f8515005F6c00Aa")
)

// Signature types: who signs for the maker address.
const (
	SigTypeEOA        = 0 // the private key's own address holds the funds
	SigTypePolyProxy  = 1 // Polymarket proxy wallet owned by the key
	SigTypeGnosisSafe = 2 // Polymarket Gnosis Safe owned by the key
	SigTypePoly1271   = 3 // deposit wallet / smart contract (EIP-1271)
)

// roundingConfig mirrors the SDK: decimals allowed for price per tick size.
var priceDecimals = map[string]int{
	"0.1": 1, "0.01": 2, "0.005": 3, "0.0025": 4, "0.001": 3, "0.0001": 4,
}

const usdcUnit = 1_000_000 // pUSD and outcome tokens both use 6 decimals

// OrderParams describes a limit order to build and sign.
type OrderParams struct {
	TokenID    string
	Side       string // "buy" or "sell"
	Price      float64
	Size       int // shares
	TickSize   string
	NegRisk    bool
	Version    int   // exchange version reported by GET /version (2 or 3)
	Expiration int64 // unix seconds for GTD, 0 otherwise
}

// SignedOrder is the order object sent in POST /order. Field order follows the SDK.
type SignedOrder struct {
	Salt          int64  `json:"salt"`
	Maker         string `json:"maker"`
	Signer        string `json:"signer"`
	TokenID       string `json:"tokenId"`
	MakerAmount   string `json:"makerAmount"`
	TakerAmount   string `json:"takerAmount"`
	Side          string `json:"side"` // "BUY" or "SELL"
	SignatureType int    `json:"signatureType"`
	Timestamp     string `json:"timestamp"` // unix milliseconds
	Expiration    string `json:"expiration"`
	Metadata      string `json:"metadata"`
	Builder       string `json:"builder"`
	Signature     string `json:"signature"`
}

// OrderBuilder builds and signs orders for one wallet.
type OrderBuilder struct {
	signer  *Signer
	maker   Address // address that holds the funds
	sigType int

	// overridable in tests
	now  func() time.Time
	salt func() int64
}

// NewOrderBuilder returns a builder. maker is the funder address; pass the
// zero address to use the signer's own address (EOA).
func NewOrderBuilder(signer *Signer, maker Address, sigType int) (*OrderBuilder, error) {
	if sigType < SigTypeEOA || sigType > SigTypePoly1271 {
		return nil, fmt.Errorf("unsupported signature_type %d (want 0-3)", sigType)
	}
	if maker == (Address{}) {
		if sigType != SigTypeEOA {
			return nil, fmt.Errorf("api.funder_address is required for signature_type %d", sigType)
		}
		maker = signer.Address()
	}
	return &OrderBuilder{signer: signer, maker: maker, sigType: sigType, now: time.Now, salt: randomSalt}, nil
}

// Maker returns the address that holds the funds and positions.
func (b *OrderBuilder) Maker() Address { return b.maker }

// Build rounds the price to the market's tick, computes the amounts and signs the order.
func (b *OrderBuilder) Build(p OrderParams) (*SignedOrder, error) {
	decimals, ok := priceDecimals[p.TickSize]
	if !ok {
		return nil, fmt.Errorf("unsupported tick size %q", p.TickSize)
	}
	tick, _ := strconv.ParseFloat(p.TickSize, 64)
	price := math.Round(p.Price*math.Pow10(decimals)) / math.Pow10(decimals)
	if price < tick || price > 1-tick {
		return nil, fmt.Errorf("price %.4f outside [%s, %.4f] for tick size %s", p.Price, p.TickSize, 1-tick, p.TickSize)
	}
	if p.Size <= 0 {
		return nil, fmt.Errorf("size must be positive")
	}
	tokenID, ok := new(big.Int).SetString(p.TokenID, 10)
	if !ok || tokenID.Sign() <= 0 {
		return nil, fmt.Errorf("token id %q is not a positive integer", p.TokenID)
	}

	// Whole shares at a price with at most 4 decimals: amounts are exact in micro-units.
	priceMicros := int64(math.Round(price * usdcUnit))
	shares := int64(p.Size) * usdcUnit
	notional := int64(p.Size) * priceMicros

	var side uint8
	var makerAmt, takerAmt int64
	switch p.Side {
	case "buy":
		side, makerAmt, takerAmt = 0, notional, shares
	case "sell":
		side, makerAmt, takerAmt = 1, shares, notional
	default:
		return nil, fmt.Errorf("invalid side %q", p.Side)
	}

	contract, domainVersion, err := exchangeFor(p.Version, p.NegRisk)
	if err != nil {
		return nil, err
	}

	signer := b.signer.Address()
	if b.sigType == SigTypePoly1271 {
		signer = b.maker // the contract wallet is the signer of record
	}
	o := orderStruct{
		salt:        b.salt(),
		maker:       b.maker,
		signer:      signer,
		tokenID:     tokenID,
		makerAmount: makerAmt,
		takerAmount: takerAmt,
		side:        side,
		sigType:     uint8(b.sigType),
		timestamp:   b.now().UnixMilli(),
	}

	domainSep := domainSeparator(domainVersion, contract)
	var sig []byte
	if b.sigType == SigTypePoly1271 {
		sig = b.sign1271(o, domainSep)
	} else {
		sig = b.signer.Sign(typedDataDigest(domainSep, o.hash()))
	}

	zero := "0x" + strings.Repeat("0", 64)
	return &SignedOrder{
		Salt:          o.salt,
		Maker:         o.maker.String(),
		Signer:        o.signer.String(),
		TokenID:       tokenID.String(),
		MakerAmount:   strconv.FormatInt(makerAmt, 10),
		TakerAmount:   strconv.FormatInt(takerAmt, 10),
		Side:          strings.ToUpper(p.Side),
		SignatureType: b.sigType,
		Timestamp:     strconv.FormatInt(o.timestamp, 10),
		Expiration:    strconv.FormatInt(p.Expiration, 10),
		Metadata:      zero,
		Builder:       zero,
		Signature:     "0x" + hex.EncodeToString(sig),
	}, nil
}

// sign1271 produces the ERC-7739 wrapped signature the exchange verifies
// against a deposit wallet: inner sig || app domain separator || contents
// hash || order type string || uint16 type string length.
func (b *OrderBuilder) sign1271(o orderStruct, domainSep []byte) []byte {
	contents := o.hash()
	tds := keccak256(
		keccak256([]byte(typedDataSignTypeString)),
		contents,
		keccak256([]byte("DepositWallet")),
		keccak256([]byte("1")),
		uint256(big.NewInt(polygonChainID)),
		addressWord(o.signer),
		make([]byte, 32), // salt
	)
	inner := b.signer.Sign(typedDataDigest(domainSep, tds))

	var lenBytes [2]byte
	binary.BigEndian.PutUint16(lenBytes[:], uint16(len(orderTypeString)))
	out := append([]byte{}, inner...)
	out = append(out, domainSep...)
	out = append(out, contents...)
	out = append(out, orderTypeString...)
	return append(out, lenBytes[:]...)
}

func exchangeFor(version int, negRisk bool) (Address, string, error) {
	switch version {
	case 2:
		if negRisk {
			return negRiskExchangeV2, "2", nil
		}
		return exchangeV2, "2", nil
	case 3:
		return exchangeV3, "3", nil
	}
	return Address{}, "", fmt.Errorf("unsupported order version %d", version)
}

type orderStruct struct {
	salt        int64
	maker       Address
	signer      Address
	tokenID     *big.Int
	makerAmount int64
	takerAmount int64
	side        uint8
	sigType     uint8
	timestamp   int64
}

// hash is the EIP-712 struct hash of the order.
func (o orderStruct) hash() []byte {
	zero := make([]byte, 32)
	return keccak256(
		keccak256([]byte(orderTypeString)),
		uint256(big.NewInt(o.salt)),
		addressWord(o.maker),
		addressWord(o.signer),
		uint256(o.tokenID),
		uint256(big.NewInt(o.makerAmount)),
		uint256(big.NewInt(o.takerAmount)),
		uint256(big.NewInt(int64(o.side))),
		uint256(big.NewInt(int64(o.sigType))),
		uint256(big.NewInt(o.timestamp)),
		zero, // metadata
		zero, // builder
	)
}

func domainSeparator(version string, contract Address) []byte {
	return keccak256(
		keccak256([]byte(domainTypeString)),
		keccak256([]byte(exchangeDomainName)),
		keccak256([]byte(version)),
		uint256(big.NewInt(polygonChainID)),
		addressWord(contract),
	)
}

func typedDataDigest(domainSep, structHash []byte) [32]byte {
	var d [32]byte
	copy(d[:], keccak256([]byte{0x19, 0x01}, domainSep, structHash))
	return d
}

func uint256(v *big.Int) []byte { return v.FillBytes(make([]byte, 32)) }

func addressWord(a Address) []byte {
	w := make([]byte, 32)
	copy(w[12:], a[:])
	return w
}

func mustAddress(s string) Address {
	a, err := ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return a
}

// randomSalt matches the SDK's range (below ~2^41) so it stays a safe JSON integer.
func randomSalt() int64 {
	var b [8]byte
	rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) >> 23)
}
