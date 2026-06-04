package chainworker

import "testing"

func TestEVMModuleFor(t *testing.T) {
	cases := map[string]string{
		"BNB":    "BNB",
		"ETH":    "ETH",
		"MATIC":  "MATIC",
		"AVAX":   "AVAX",
		"ARBETH": "ARBETH",
		"OPETH":  "OPETH",
	}
	for module, native := range cases {
		got, ok := evmModuleFor(module)
		if !ok {
			t.Fatalf("%s should be an EVM module", module)
		}
		if got.NativeCrypto != native {
			t.Fatalf("%s native crypto mismatch: %s", module, got.NativeCrypto)
		}
	}
	if _, ok := evmModuleFor("TRON"); ok {
		t.Fatalf("TRON must not be treated as EVM")
	}
	if _, ok := evmModuleFor("SOL"); ok {
		t.Fatalf("SOL must not be treated as EVM")
	}
}

func TestBitcoinLikeModuleFor(t *testing.T) {
	for _, module := range []string{"BTC", "LTC", "DOGE", "FIRO"} {
		if !isBitcoinLikeModule(module) {
			t.Fatalf("%s should be treated as a Bitcoin-like JSON-RPC module", module)
		}
	}
	for _, module := range []string{"BTC-LIGHTNING", "TRON", "BNB", "SOL", "XMR", "XRP"} {
		if isBitcoinLikeModule(module) {
			t.Fatalf("%s must not be treated as a Bitcoin-like module", module)
		}
	}
}

func TestDefaultEVMChainIDs(t *testing.T) {
	cases := map[string]int64{
		"ETH":    1,
		"BNB":    56,
		"MATIC":  137,
		"AVAX":   43114,
		"ARBETH": 42161,
		"OPETH":  10,
	}
	for module, chainID := range cases {
		if got := defaultEVMChainID(module); got != chainID {
			t.Fatalf("%s chain id mismatch: %d", module, got)
		}
	}
}

func TestTokenConfigRequiresDecimalsForCustomToken(t *testing.T) {
	t.Setenv("ETH_USDT_CONTRACT", "0x0000000000000000000000000000000000000001")
	if _, _, err := tokenConfig("ETH-USDT"); err == nil {
		t.Fatalf("custom token config without decimals should fail")
	}
	t.Setenv("ETH_USDT_DECIMALS", "6")
	contract, decimals, err := tokenConfig("ETH-USDT")
	if err != nil {
		t.Fatalf("token config with decimals: %v", err)
	}
	if contract != "0x0000000000000000000000000000000000000001" || decimals != 6 {
		t.Fatalf("unexpected custom token config: %s %d", contract, decimals)
	}
}
