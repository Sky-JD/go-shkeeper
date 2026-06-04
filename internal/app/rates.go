package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type RateService struct {
	cfg        Config
	store      *Store
	logger     *slog.Logger
	httpClient *http.Client
}

func NewRateService(cfg Config, store *Store, logger *slog.Logger) *RateService {
	return &RateService{
		cfg:        cfg,
		store:      store,
		logger:     logger,
		httpClient: &http.Client{Timeout: cfg.RequestTimeout},
	}
}

func (s *RateService) Convert(ctx context.Context, amountFiat decimal.Decimal, fiat string, module *CryptoModule) (decimal.Decimal, decimal.Decimal, error) {
	rate, err := s.store.ExchangeRate(ctx, fiat, module.Name)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	currentRate, err := s.CurrentRate(ctx, rate)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	fee := s.fee(amountFiat, rate)
	converted := amountFiat.Add(fee).Div(currentRate).Round(module.Precision)
	return converted, currentRate, nil
}

func (s *RateService) CurrentRate(ctx context.Context, rate ExchangeRate) (decimal.Decimal, error) {
	source := strings.ToLower(rate.Source)
	if source == "" || source == "dynamic" {
		source = "binance"
	}
	if source == "manual" {
		if rate.Rate.IsZero() {
			return decimal.Zero, fmt.Errorf("manual rate for %s/%s is zero", rate.Crypto, rate.Fiat)
		}
		return rate.Rate, nil
	}
	if rate.Fiat == "USD" && (isUSDT(rate.Crypto) || isUSDC(rate.Crypto)) {
		return decimal.NewFromInt(1), nil
	}
	base := normalizeRateSymbol(rate.Crypto)
	fiat := rate.Fiat
	switch source {
	case "coinbase":
		return s.coinbase(ctx, base, fiat)
	case "kraken":
		return s.kraken(ctx, base, fiat)
	case "kucoin":
		return s.kucoin(ctx, base, fiat)
	default:
		return s.binance(ctx, base, fiat)
	}
}

func (s *RateService) fee(amount decimal.Decimal, rate ExchangeRate) decimal.Decimal {
	policy := rate.FeePolicy
	if policy == "" {
		policy = "PERCENT_FEE"
	}
	percent := amount.Mul(rate.Fee).Div(decimal.NewFromInt(100))
	switch policy {
	case "NO_FEE":
		return decimal.Zero
	case "FIXED_FEE":
		return rate.FixedFee
	case "PERCENT_OR_MINIMAL_FIXED_FEE":
		if percent.LessThan(rate.FixedFee) {
			return rate.FixedFee
		}
		return percent
	default:
		return percent
	}
}

func (s *RateService) OriginalAmount(amountWithFee decimal.Decimal, rate ExchangeRate) decimal.Decimal {
	policy := rate.FeePolicy
	if policy == "" {
		policy = "PERCENT_FEE"
	}
	withoutPercent := amountWithFee.Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(100).Add(rate.Fee))
	switch policy {
	case "NO_FEE":
		return amountWithFee
	case "FIXED_FEE":
		return amountWithFee.Sub(rate.FixedFee)
	case "PERCENT_OR_MINIMAL_FIXED_FEE":
		fixed := amountWithFee.Sub(rate.FixedFee)
		if withoutPercent.LessThan(fixed) {
			return withoutPercent
		}
		return fixed
	default:
		return withoutPercent
	}
}

func (s *RateService) binance(ctx context.Context, crypto, fiat string) (decimal.Decimal, error) {
	if fiat == "USD" {
		fiat = "USDT"
	}
	var payload struct {
		Price string `json:"price"`
	}
	url := fmt.Sprintf("https://api.binance.com/api/v3/ticker/price?symbol=%s%s", crypto, fiat)
	if err := s.getJSON(ctx, url, &payload); err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(payload.Price)
}

func (s *RateService) coinbase(ctx context.Context, crypto, fiat string) (decimal.Decimal, error) {
	if fiat == "USD" {
		fiat = "USDT"
	}
	var payload struct {
		Data struct {
			Rates map[string]string `json:"rates"`
		} `json:"data"`
	}
	url := fmt.Sprintf("https://api.coinbase.com/v2/exchange-rates?currency=%s", crypto)
	if err := s.getJSON(ctx, url, &payload); err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(payload.Data.Rates[fiat])
}

func (s *RateService) kraken(ctx context.Context, crypto, fiat string) (decimal.Decimal, error) {
	if fiat == "USD" {
		fiat = "USDT"
	}
	var payload struct {
		Error  []string `json:"error"`
		Result map[string]struct {
			C []string `json:"c"`
		} `json:"result"`
	}
	url := fmt.Sprintf("https://api.kraken.com/0/public/Ticker?pair=%s%s", crypto, fiat)
	if err := s.getJSON(ctx, url, &payload); err != nil {
		return decimal.Zero, err
	}
	if len(payload.Error) > 0 {
		return decimal.Zero, fmt.Errorf("%s", strings.Join(payload.Error, "; "))
	}
	for _, row := range payload.Result {
		if len(row.C) > 0 {
			return decimal.NewFromString(row.C[0])
		}
	}
	return decimal.Zero, fmt.Errorf("empty kraken rate for %s/%s", crypto, fiat)
}

func (s *RateService) kucoin(ctx context.Context, crypto, fiat string) (decimal.Decimal, error) {
	var payload struct {
		Code string            `json:"code"`
		Data map[string]string `json:"data"`
	}
	url := fmt.Sprintf("https://api.kucoin.com/api/v1/prices?base=%s&currencies=%s", fiat, crypto)
	if err := s.getJSON(ctx, url, &payload); err != nil {
		return decimal.Zero, err
	}
	if payload.Code != "200000" {
		return decimal.Zero, fmt.Errorf("kucoin returned code %s", payload.Code)
	}
	return decimal.NewFromString(payload.Data[crypto])
}

func (s *RateService) getJSON(ctx context.Context, url string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("rate provider returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func normalizeRateSymbol(crypto string) string {
	switch {
	case isUSDT(crypto):
		return "USDT"
	case isUSDC(crypto):
		return "USDC"
	case crypto == "BTC-LIGHTNING":
		return "BTC"
	case crypto == "FIRO-SPARK":
		return "FIRO"
	case crypto == "ARBETH" || crypto == "OPETH":
		return "ETH"
	case crypto == "ARB-TOKEN":
		return "ARB"
	case crypto == "OP-TOKEN":
		return "OP"
	default:
		return crypto
	}
}

func isUSDT(crypto string) bool {
	switch crypto {
	case "USDT", "ETH-USDT", "BNB-USDT", "POLYGON-USDT", "AVALANCHE-USDT", "SOLANA-USDT", "OP-USDT":
		return true
	default:
		return false
	}
}

func isUSDC(crypto string) bool {
	switch crypto {
	case "USDC", "ETH-USDC", "BNB-USDC", "POLYGON-USDC", "AVALANCHE-USDC", "SOLANA-USDC", "ARB-USDC", "OP-USDC":
		return true
	default:
		return false
	}
}
