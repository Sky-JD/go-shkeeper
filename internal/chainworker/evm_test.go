package chainworker

import (
	"encoding/hex"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestAmountToBaseUnits(t *testing.T) {
	got := amountToBaseUnits(decimal.RequireFromString("1.234567"), 6).String()
	if got != "1234567" {
		t.Fatalf("unexpected base units: %s", got)
	}
	truncated := amountToBaseUnits(decimal.RequireFromString("1.23456789"), 6).String()
	if truncated != "1234567" {
		t.Fatalf("unexpected truncated base units: %s", truncated)
	}
}

func TestEVMCallData(t *testing.T) {
	address, err := evmAddressBytes("0x000000000000000000000000000000000000dead")
	if err != nil {
		t.Fatalf("evm address: %v", err)
	}
	balanceOf := hex.EncodeToString(evmCallData("balanceOf(address)", address))
	if !strings.HasPrefix(balanceOf, "70a08231") {
		t.Fatalf("unexpected balanceOf selector: %s", balanceOf[:8])
	}
	if len(balanceOf) != 8+64 {
		t.Fatalf("unexpected balanceOf length: %d", len(balanceOf))
	}
	if !strings.HasSuffix(balanceOf, "000000000000000000000000000000000000dead") {
		t.Fatalf("unexpected balanceOf address word: %s", balanceOf)
	}

	transfer := hex.EncodeToString(evmCallData("transfer(address,uint256)", address, amountToBaseUnits(decimal.NewFromInt(10), 18).Bytes()))
	if !strings.HasPrefix(transfer, "a9059cbb") {
		t.Fatalf("unexpected transfer selector: %s", transfer[:8])
	}
	if len(transfer) != 8+64+64 {
		t.Fatalf("unexpected transfer length: %d", len(transfer))
	}
}

func TestBNBTokenConfigEnvOverride(t *testing.T) {
	t.Setenv("BNB_USDT_CONTRACT", "0x0000000000000000000000000000000000000001")
	t.Setenv("BNB_USDT_DECIMALS", "6")
	contract, decimals, err := tokenConfig("BNB-USDT")
	if err != nil {
		t.Fatalf("token config: %v", err)
	}
	if contract != os.Getenv("BNB_USDT_CONTRACT") || decimals != 6 {
		t.Fatalf("unexpected token config: %s %d", contract, decimals)
	}
}

func TestSignLegacyEVMTransaction(t *testing.T) {
	to, err := evmAddressBytes("0x000000000000000000000000000000000000dead")
	if err != nil {
		t.Fatalf("evm address: %v", err)
	}
	_, privateKeyHex, err := newBNBAccount()
	if err != nil {
		t.Fatalf("new bnb account: %v", err)
	}
	raw, txHash, err := signLegacyEVMTransaction(privateKeyHex, 1, big.NewInt(3_000_000_000), 21_000, to, big.NewInt(1), nil, big.NewInt(56))
	if err != nil {
		t.Fatalf("sign evm transaction: %v", err)
	}
	if len(raw) == 0 || raw[0] < 0xc0 {
		t.Fatalf("unexpected raw RLP: %x", raw)
	}
	if len(txHash) != 32 {
		t.Fatalf("unexpected tx hash length: %d", len(txHash))
	}
}

func TestEVMTopicAddress(t *testing.T) {
	topic := "0x000000000000000000000000000000000000000000000000000000000000dead"
	if got := evmTopicAddress(topic); got != "0x000000000000000000000000000000000000dead" {
		t.Fatalf("unexpected topic address: %s", got)
	}
}
