package chainworker

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestNewSolanaAddressRoundTrip(t *testing.T) {
	s := &Server{}
	address, privateKey, err := s.newSolanaAddress()
	if err != nil {
		t.Fatalf("new solana address: %v", err)
	}
	pubKey, err := solanaPubKeyFromBase58(address)
	if err != nil {
		t.Fatalf("decode public key: %v", err)
	}
	key, err := solanaPrivateKeyFromBase58(privateKey)
	if err != nil {
		t.Fatalf("decode private key: %v", err)
	}
	if got := solanaPubKeyFromPrivate(key); got != pubKey {
		t.Fatalf("private key does not match address: got=%s want=%s", got.String(), pubKey.String())
	}
}

func TestSolanaTokenConfigDefaultsAndOverrides(t *testing.T) {
	usdc, err := solanaTokenConfig("SOLANA-USDC")
	if err != nil {
		t.Fatalf("usdc config: %v", err)
	}
	if usdc.Mint != "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v" || usdc.Decimals != 6 || usdc.TokenProgram != solanaTokenProgramBase58 {
		t.Fatalf("unexpected usdc config: %+v", usdc)
	}
	pyusd, err := solanaTokenConfig("SOLANA-PYUSD")
	if err != nil {
		t.Fatalf("pyusd config: %v", err)
	}
	if pyusd.Mint != "2b1kV6DkPAnxd5ixfnxCpjxmKwqjjaYmCZfHsFu24GXo" || pyusd.TokenProgram != solanaToken2022Base58 {
		t.Fatalf("unexpected pyusd config: %+v", pyusd)
	}

	t.Setenv("SOLANA_USDT_MINT", "11111111111111111111111111111111")
	t.Setenv("SOLANA_USDT_DECIMALS", "9")
	t.Setenv("SOLANA_USDT_TOKEN_PROGRAM", solanaToken2022Base58)
	usdt, err := solanaTokenConfig("SOLANA-USDT")
	if err != nil {
		t.Fatalf("custom usdt config: %v", err)
	}
	if usdt.Mint != "11111111111111111111111111111111" || usdt.Decimals != 9 || usdt.TokenProgram != solanaToken2022Base58 {
		t.Fatalf("unexpected custom usdt config: %+v", usdt)
	}
}

func TestSolanaStaticProgramIDsAreValid(t *testing.T) {
	cases := []struct {
		name  string
		value string
		got   solanaPubKey
	}{
		{name: "system", value: solanaSystemProgramBase58, got: solanaSystemProgramID},
		{name: "token", value: solanaTokenProgramBase58, got: solanaTokenProgramID},
		{name: "token-2022", value: solanaToken2022Base58, got: solanaToken2022ProgramID},
		{name: "associated-token", value: solanaAssociatedTokenBase58, got: solanaAssociatedTokenID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := solanaPubKeyFromBase58(tc.value)
			if err != nil {
				t.Fatalf("decode static program id: %v", err)
			}
			if decoded != tc.got || tc.got.String() != tc.value {
				t.Fatalf("program id mismatch: got=%s want=%s", tc.got.String(), tc.value)
			}
		})
	}
}

func TestSolanaAssociatedTokenAddressMatchesSDKFixture(t *testing.T) {
	owner := mustSolanaPubKey(t, "7EcDhS6D6tzi1to5o2eCY96vK99MfU6hQ4mLbk3L9maA")
	cases := []struct {
		name         string
		mint         string
		tokenProgram string
		want         string
	}{
		{
			name:         "usdc",
			mint:         "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
			tokenProgram: solanaTokenProgramBase58,
			want:         "6Y9TwVJqb8tpF2QryN4df4GbG3iJAaaKn779C8jZwf8n",
		},
		{
			name:         "usdt",
			mint:         "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB",
			tokenProgram: solanaTokenProgramBase58,
			want:         "9W9K7YD2nn4vhNZ6HUk93qAfQy7xXhuQ4BKA3pAA5MLH",
		},
		{
			name:         "pyusd-token-2022",
			mint:         "2b1kV6DkPAnxd5ixfnxCpjxmKwqjjaYmCZfHsFu24GXo",
			tokenProgram: solanaToken2022Base58,
			want:         "4pRmR3TwVqAt1U9KddqdjNaB8dEFYWtiqXqP45uC5S9Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mint := mustSolanaPubKey(t, tc.mint)
			program := mustSolanaPubKey(t, tc.tokenProgram)
			ata, err := solanaAssociatedTokenAddress(owner, mint, program)
			if err != nil {
				t.Fatalf("ata: %v", err)
			}
			if ata.String() != tc.want {
				t.Fatalf("unexpected ata: got=%s want=%s", ata.String(), tc.want)
			}
		})
	}
}

func TestSolanaWorkerRPCBridge(t *testing.T) {
	signer := &Server{}
	address, privateKey, err := signer.newSolanaAddress()
	if err != nil {
		t.Fatalf("new solana account: %v", err)
	}
	encryptedKey, err := encryptSecret("account-password", privateKey)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	destination, _, err := signer.newSolanaAddress()
	if err != nil {
		t.Fatalf("new destination: %v", err)
	}
	blockhashBytes := make([]byte, 32)
	copy(blockhashBytes, []byte("go-shkeeper-solana-test-blockhash"))
	blockhash := base58Encode(blockhashBytes)

	var sawSendTransaction bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		var result any
		switch req.Method {
		case "getSlot":
			result = 100
		case "getBlockTime":
			result = 1780500000
		case "getBalance":
			result = map[string]any{"value": 2500000000}
		case "getTokenAccountsByOwner":
			result = map[string]any{"value": []any{
				map[string]any{"account": map[string]any{"data": map[string]any{"parsed": map[string]any{"info": map[string]any{
					"tokenAmount": map[string]any{"amount": "1234500", "decimals": 6},
				}}}}},
			}}
		case "getLatestBlockhash":
			result = map[string]any{"value": map[string]any{"blockhash": blockhash}}
		case "sendTransaction":
			var params []any
			if err := json.Unmarshal(req.Params, &params); err != nil {
				t.Fatalf("decode send params: %v", err)
			}
			raw, ok := params[0].(string)
			if !ok || raw == "" {
				t.Fatalf("sendTransaction missing base64 tx: %+v", params)
			}
			if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
				t.Fatalf("sendTransaction tx is not base64: %v", err)
			}
			sawSendTransaction = true
			result = "solana-submit-signature"
		default:
			t.Fatalf("unexpected solana method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "go-chain-worker", "result": result})
	}))
	defer server.Close()

	s := &Server{
		cfg:    Config{Module: "SOL", FullnodeURL: server.URL, AccountPassword: "account-password"},
		client: server.Client(),
	}
	status, err := s.solanaStatus(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["last_block_timestamp"] != int64(1780500000) && status["last_block_timestamp"] != 1780500000 {
		t.Fatalf("unexpected status: %+v", status)
	}
	nativeBalance, err := s.solanaBalance(t.Context(), "SOL", address)
	if err != nil || !nativeBalance.Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("unexpected native balance %s err=%v", nativeBalance, err)
	}
	tokenBalance, err := s.solanaBalance(t.Context(), "SOLANA-USDC", address)
	if err != nil || !tokenBalance.Equal(decimal.RequireFromString("1.2345")) {
		t.Fatalf("unexpected token balance %s err=%v", tokenBalance, err)
	}
	payout, err := s.signAndBroadcastSolana(t.Context(), Account{Module: "SOL", Crypto: "SOL", Address: address, PrivateKeyHex: encryptedKey}, "SOL", destination, decimal.RequireFromString("0.01"))
	if err != nil {
		t.Fatalf("payout: %v", err)
	}
	if payout != "solana-submit-signature" || !sawSendTransaction {
		t.Fatalf("unexpected payout signature=%s saw=%v", payout, sawSendTransaction)
	}
}

func TestSolanaTransferParsing(t *testing.T) {
	owner := "7EcDhS6D6tzi1to5o2eCY96vK99MfU6hQ4mLbk3L9maA"
	ownerKey := solanaAccountKey(owner)
	tx := solanaTransactionResult{Slot: 99}
	tx.Transaction.Message.AccountKeys = []solanaAccountKey{ownerKey, "6Y9TwVJqb8tpF2QryN4df4GbG3iJAaaKn779C8jZwf8n"}
	tx.Meta.PreBalances = []json.Number{"1000000000", "0"}
	tx.Meta.PostBalances = []json.Number{"2500000000", "0"}
	native := solanaNativeTransfers(tx, 100, map[string]struct{}{strings.ToLower(owner): {}})
	if len(native) != 1 || native[0].Amount != "1.5" || native[0].Category != "receive" || native[0].Confirmations != 2 {
		t.Fatalf("unexpected native transfers: %+v", native)
	}

	usdc, err := solanaTokenConfig("SOLANA-USDC")
	if err != nil {
		t.Fatalf("usdc config: %v", err)
	}
	tx.Meta.PreTokenBalances = []solanaTokenBalance{solanaTestTokenBalance(1, usdc.Mint, owner, "1000000")}
	tx.Meta.PostTokenBalances = []solanaTokenBalance{solanaTestTokenBalance(1, usdc.Mint, owner, "2500000")}
	token, err := solanaTokenTransfers(tx, 100, map[string]struct{}{strings.ToLower(owner): {}}, usdc)
	if err != nil {
		t.Fatalf("token transfers: %v", err)
	}
	if len(token) != 1 || token[0].Amount != "1.5" || token[0].Category != "receive" || token[0].Address != owner {
		t.Fatalf("unexpected token transfers: %+v", token)
	}
}

func solanaTestTokenBalance(index int, mint string, owner string, amount string) solanaTokenBalance {
	var row solanaTokenBalance
	row.AccountIndex = index
	row.Mint = mint
	row.Owner = owner
	row.TokenAmount.Amount = amount
	row.TokenAmount.Decimals = 6
	return row
}

func mustSolanaPubKey(t *testing.T, value string) solanaPubKey {
	t.Helper()
	pubKey, err := solanaPubKeyFromBase58(value)
	if err != nil {
		t.Fatalf("decode solana pubkey %q: %v", value, err)
	}
	return pubKey
}
