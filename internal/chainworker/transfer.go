package chainworker

import (
	"context"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

const evmTransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

type transferResult struct {
	Address       string `json:"address"`
	Amount        string `json:"amount"`
	Confirmations int64  `json:"confirmations"`
	Category      string `json:"category"`
}

func (s *Server) latestBlockNumber(ctx context.Context) (int64, error) {
	if s.isEVMModule() {
		var blockHex string
		if err := s.rpc(ctx, "eth_blockNumber", []any{}, &blockHex); err != nil {
			return 0, err
		}
		return hexToInt(blockHex)
	}
	switch s.cfg.Module {
	case "SOL":
		return s.solanaLatestBlockNumber(ctx)
	case "TRON":
		var block struct {
			BlockHeader struct {
				RawData struct {
					Number int64 `json:"number"`
				} `json:"raw_data"`
			} `json:"block_header"`
		}
		if err := s.httpJSON(ctx, httpMethodPost, strings.TrimRight(s.fullnodeURL(), "/")+"/wallet/getnowblock", nil, &block); err != nil {
			return 0, err
		}
		if block.BlockHeader.RawData.Number <= 0 {
			return 0, fmt.Errorf("tron fullnode returned no block number")
		}
		return block.BlockHeader.RawData.Number, nil
	default:
		return 0, fmt.Errorf("unsupported chain module: %s", s.cfg.Module)
	}
}

func confirmations(latest int64, blockNumber int64) int64 {
	if latest <= 0 || blockNumber <= 0 || latest < blockNumber {
		return 0
	}
	return latest - blockNumber + 1
}

func (s *Server) accountSet(ctx context.Context, crypto string) (map[string]struct{}, error) {
	accounts, err := s.store.ListAccounts(ctx, s.cfg.Module, crypto)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 && !s.isNative(crypto) {
		accounts, err = s.store.AccountsByModule(ctx, s.cfg.Module)
		if err != nil {
			return nil, err
		}
	}
	out := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if s.isEVMModule() {
			out[strings.ToLower(account.Address)] = struct{}{}
			continue
		}
		switch s.cfg.Module {
		case "SOL":
			out[strings.ToLower(account.Address)] = struct{}{}
		case "TRON":
			if hexAddress, err := tronAddressHex(account.Address); err == nil {
				out[strings.ToLower(hexAddress)] = struct{}{}
			}
			out[account.Address] = struct{}{}
		}
	}
	return out, nil
}

func transferCategory(fromOwned bool, toOwned bool) string {
	switch {
	case toOwned:
		return "receive"
	case fromOwned:
		return "send"
	default:
		return ""
	}
}

func decimalFromBaseUnits(value decimal.Decimal, decimals int32) decimal.Decimal {
	return value.Div(decimal.New(1, decimals))
}
