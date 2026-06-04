package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCryptoDefinitionsCoverLegacyCryptoModules(t *testing.T) {
	defs := cryptoDefinitions()
	byName := make(map[string]CryptoModule, len(defs))
	for _, def := range defs {
		if _, exists := byName[def.Name]; exists {
			t.Fatalf("duplicate crypto definition: %s", def.Name)
		}
		byName[def.Name] = def
	}

	legacyModules := []string{
		"ARBETH",
		"ARB-PYUSD",
		"ARB-TOKEN",
		"ARB-USDC",
		"AVALANCHE-USDC",
		"AVALANCHE-USDT",
		"AVAX",
		"BTC-LIGHTNING",
		"BNB",
		"BNB-USDC",
		"BNB-USDT",
		"BTC",
		"DOGE",
		"ETH",
		"ETH-PYUSD",
		"ETH-USDC",
		"ETH-USDT",
		"FIRO",
		"FIRO-SPARK",
		"LTC",
		"MATIC",
		"XMR",
		"OPETH",
		"OP-TOKEN",
		"OP-USDC",
		"OP-USDT",
		"POLYGON-USDC",
		"POLYGON-USDT",
		"SOL",
		"SOLANA-PYUSD",
		"SOLANA-USDC",
		"SOLANA-USDT",
		"TRX",
		"USDC",
		"USDT",
		"XRP",
	}
	for _, name := range legacyModules {
		if _, ok := byName[name]; !ok {
			t.Fatalf("legacy crypto module %s is missing from Go definitions", name)
		}
	}
}

func TestCryptoDefinitionsCoverCurrentLegacyPythonCryptoFiles(t *testing.T) {
	legacyModules := legacyPythonCryptoNames(t)
	defs := cryptoDefinitions()
	byName := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		byName[def.Name] = struct{}{}
	}
	for _, name := range legacyModules {
		if _, ok := byName[name]; !ok {
			t.Fatalf("legacy Python crypto %s is missing from Go definitions", name)
		}
	}
}

func TestHKModularDefaultCryptoListCoversCurrentLegacyPythonCryptoFiles(t *testing.T) {
	legacyModules := legacyPythonCryptoNames(t)
	body, err := os.ReadFile("../../deploy/hk-16-16.modular.example.yml")
	if err != nil {
		t.Fatalf("read modular compose: %v", err)
	}
	enabled := parseDefaultComposeCryptos(t, string(body))
	for _, name := range legacyModules {
		if _, ok := enabled[name]; !ok {
			t.Fatalf("hk modular compose default SHKEEPER_CRYPTOS is missing legacy Python crypto %s", name)
		}
	}
}

func TestBackendCryptoDefaultsUseModularWorkerHosts(t *testing.T) {
	expected := map[string]string{
		"BTC":            "btc-worker",
		"BTC-LIGHTNING":  "btc-lightning-worker",
		"LTC":            "ltc-worker",
		"DOGE":           "doge-worker",
		"FIRO":           "firo-worker",
		"FIRO-SPARK":     "firo-worker",
		"ETH":            "eth-worker",
		"ETH-USDT":       "eth-worker",
		"ETH-USDC":       "eth-worker",
		"ETH-PYUSD":      "eth-worker",
		"TRX":            "tron-worker",
		"USDT":           "tron-worker",
		"USDC":           "tron-worker",
		"BNB":            "bnb-worker",
		"BNB-USDT":       "bnb-worker",
		"BNB-USDC":       "bnb-worker",
		"MATIC":          "polygon-worker",
		"POLYGON-USDT":   "polygon-worker",
		"POLYGON-USDC":   "polygon-worker",
		"AVAX":           "avalanche-worker",
		"AVALANCHE-USDT": "avalanche-worker",
		"AVALANCHE-USDC": "avalanche-worker",
		"SOL":            "solana-worker",
		"SOLANA-USDT":    "solana-worker",
		"SOLANA-USDC":    "solana-worker",
		"SOLANA-PYUSD":   "solana-worker",
		"XRP":            "xrp-worker",
		"ARBETH":         "arbitrum-worker",
		"ARB-USDC":       "arbitrum-worker",
		"ARB-PYUSD":      "arbitrum-worker",
		"ARB-TOKEN":      "arbitrum-worker",
		"OPETH":          "optimism-worker",
		"OP-USDT":        "optimism-worker",
		"OP-USDC":        "optimism-worker",
		"OP-TOKEN":       "optimism-worker",
		"XMR":            "xmr-worker",
	}
	for _, def := range cryptoDefinitions() {
		if def.Adapter != "backend" {
			continue
		}
		want, ok := expected[def.Name]
		if !ok {
			t.Fatalf("missing expected backend host assertion for %s", def.Name)
		}
		if def.DefaultHost != want {
			t.Fatalf("%s default host=%s, want %s", def.Name, def.DefaultHost, want)
		}
	}
}

func TestBackendPayoutPathFeeCoversBitcoinLikeWorkers(t *testing.T) {
	feeModules := map[string]bool{
		"BTC":           true,
		"BTC-LIGHTNING": true,
		"LTC":           true,
		"DOGE":          true,
		"FIRO":          true,
		"FIRO-SPARK":    true,
	}
	for _, def := range cryptoDefinitions() {
		got := backendPayoutUsesPathFee(&def)
		want := feeModules[def.Name]
		if got != want {
			t.Fatalf("%s backend path fee=%v, want %v", def.Name, got, want)
		}
	}
}

func legacyPythonCryptoNames(t *testing.T) []string {
	t.Helper()
	dir := filepath.Clean("../../../shkeeper/modules/cryptos")
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("legacy Python crypto dir is not present in this package checkout: %s", dir)
		}
		t.Fatalf("read legacy crypto dir: %v", err)
	}
	pattern := regexp.MustCompile(`self\.crypto\s*=\s*"([^"]+)"`)
	out := make([]string, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".py") || file.Name() == "__init__.py" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			t.Fatalf("read legacy crypto file %s: %v", file.Name(), err)
		}
		match := pattern.FindSubmatch(body)
		if match == nil {
			t.Fatalf("legacy crypto file %s does not assign self.crypto", file.Name())
		}
		out = append(out, string(match[1]))
	}
	if len(out) == 0 {
		t.Fatalf("no legacy Python crypto modules found")
	}
	return out
}

func parseDefaultComposeCryptos(t *testing.T, text string) map[string]struct{} {
	t.Helper()
	pattern := regexp.MustCompile(`SHKEEPER_CRYPTOS:\s*"\$\{SHKEEPER_CRYPTOS:-([^"}]+)\}"`)
	match := pattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("compose file does not include a default SHKEEPER_CRYPTOS list")
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(match[1], ",") {
		name := strings.TrimSpace(part)
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}
