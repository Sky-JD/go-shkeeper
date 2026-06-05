package chainworker

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestDecryptLegacyFernetSecret(t *testing.T) {
	token := "Z0FBQUFBQnFJUm1PM3ZzcUZCeWFLVXVscF9aeEY5WWFsUjlpdnRHVjQtdnZtV21pNTdmRVRQcXg2ZlVYSUxKM2daRFVheF9Ba0VhZC14UFFWVHd4SjZSOXVsUFdvdnFrMG9CVTlqSnpPbHB0VEpKZjl2SXN4SUk9"
	got, err := decryptLegacyFernetSecret("account-password", token)
	if err != nil {
		t.Fatalf("decrypt legacy fernet: %v", err)
	}
	if got != "0123456789abcdef" {
		t.Fatalf("unexpected plaintext: %s", got)
	}
}

func TestImportLegacyAccountsJSONDecryptsAndReencryptsBNBWallets(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	token := "Z0FBQUFBQnFJUm1PM3ZzcUZCeWFLVXVscF9aeEY5WWFsUjlpdnRHVjQtdnZtV21pNTdmRVRQcXg2ZlVYSUxKM2daRFVheF9Ba0VhZC14UFFWVHd4SjZSOXVsUFdvdnFrMG9CVTlqSnpPbHB0VEpKZjl2SXN4SUk9"
	dump := `{
	  "tables": {
	    "wallets": [
	      {"id": 1, "pub_address": "0x1111111111111111111111111111111111111111", "crypto": "BNB-USDT", "priv_key": "` + token + `", "create_time": "2026-06-02 15:46:56", "type": "regular"},
	      {"id": 2, "pub_address": "0x2222222222222222222222222222222222222222", "priv_key": "` + token + `", "create_time": "2026-06-02 15:48:03", "type": "fee_deposit"}
	    ]
	  }
	}`

	report, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		Module:                "BNB",
		LegacyAccountPassword: "account-password",
		AccountPassword:       "new-account-password",
	})
	if err != nil {
		t.Fatalf("import legacy accounts: %v", err)
	}
	if report.Rows != 2 || report.Modules["BNB"] != 2 || report.Cryptos["BNB-USDT"] != 1 || report.Cryptos["BNB"] != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	regular, err := store.ListAccounts(ctx, "BNB", "BNB-USDT")
	if err != nil {
		t.Fatalf("list regular accounts: %v", err)
	}
	if len(regular) != 1 {
		t.Fatalf("unexpected regular accounts: %+v", regular)
	}
	decrypted, err := decryptSecret("new-account-password", regular[0].PrivateKeyHex)
	if err != nil {
		t.Fatalf("decrypt imported secret: %v", err)
	}
	if decrypted != "0123456789abcdef" {
		t.Fatalf("unexpected imported secret: %s", decrypted)
	}
	feeDeposit, err := store.ListAccounts(ctx, "BNB", "BNB")
	if err != nil {
		t.Fatalf("list fee-deposit accounts: %v", err)
	}
	if len(feeDeposit) != 1 || feeDeposit[0].Address != "0x2222222222222222222222222222222222222222" {
		t.Fatalf("unexpected fee-deposit accounts: %+v", feeDeposit)
	}
}

func TestImportLegacyAccountsJSONReadsAddressKeyedExport(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	token := "Z0FBQUFBQnFJUm1PM3ZzcUZCeWFLVXVscF9aeEY5WWFsUjlpdnRHVjQtdnZtV21pNTdmRVRQcXg2ZlVYSUxKM2daRFVheF9Ba0VhZC14UFFWVHd4SjZSOXVsUFdvdnFrMG9CVTlqSnpPbHB0VEpKZjl2SXN4SUk9"
	dump := `{
	  "0x4444444444444444444444444444444444444444": {
	    "public_address": "0x4444444444444444444444444444444444444444",
	    "secret": "` + token + `"
	  },
	  "0x5555555555555555555555555555555555555555": {
	    "secret": "` + token + `"
	  }
	}`

	report, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		Module:                "BNB",
		LegacyAccountPassword: "account-password",
		AccountPassword:       "new-account-password",
	})
	if err != nil {
		t.Fatalf("import legacy accounts: %v", err)
	}
	if report.Rows != 2 || report.Modules["BNB"] != 2 || report.Cryptos["BNB-USDT"] != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	accounts, err := store.ListAccounts(ctx, "BNB", "BNB-USDT")
	if err != nil {
		t.Fatalf("list imported accounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("unexpected imported accounts: %+v", accounts)
	}
	for _, account := range accounts {
		decrypted, err := decryptSecret("new-account-password", account.PrivateKeyHex)
		if err != nil {
			t.Fatalf("decrypt imported secret: %v", err)
		}
		if decrypted != "0123456789abcdef" {
			t.Fatalf("unexpected imported secret: %s", decrypted)
		}
	}
}

func TestImportLegacyAccountsJSONReadsSingleBNBWalletObject(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	dump := `{
	  "public_address": "0x6666666666666666666666666666666666666666",
	  "secret": "plain-bnb-private-key"
	}`

	report, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		Module:          "BNB",
		DefaultCrypto:   "BNB-USDT",
		AccountPassword: "new-account-password",
	})
	if err != nil {
		t.Fatalf("import single bnb wallet object: %v", err)
	}
	if report.Rows != 1 || report.Modules["BNB"] != 1 || report.Cryptos["BNB-USDT"] != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	accounts, err := store.ListAccounts(ctx, "BNB", "BNB-USDT")
	if err != nil {
		t.Fatalf("list imported accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Address != "0x6666666666666666666666666666666666666666" {
		t.Fatalf("unexpected imported accounts: %+v", accounts)
	}
	decrypted, err := decryptSecret("new-account-password", accounts[0].PrivateKeyHex)
	if err != nil {
		t.Fatalf("decrypt imported secret: %v", err)
	}
	if decrypted != "plain-bnb-private-key" {
		t.Fatalf("unexpected imported secret: %s", decrypted)
	}
}

func TestImportLegacyAccountsJSONMapsBNBFeeDepositToNative(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	dump := `{
	  "public_address": "0x7777777777777777777777777777777777777777",
	  "secret": "plain-bnb-private-key",
	  "type": "fee_deposit"
	}`

	report, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		Module:          "BNB",
		DefaultCrypto:   "BNB-USDT",
		AccountPassword: "new-account-password",
	})
	if err != nil {
		t.Fatalf("import bnb fee deposit: %v", err)
	}
	if report.Rows != 1 || report.Modules["BNB"] != 1 || report.Cryptos["BNB"] != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	accounts, err := store.ListAccounts(ctx, "BNB", "BNB")
	if err != nil {
		t.Fatalf("list native bnb accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Address != "0x7777777777777777777777777777777777777777" {
		t.Fatalf("fee deposit was not imported as native BNB: %+v", accounts)
	}
}

func TestImportLegacyAccountsJSONRejectsTRONAddressForBNB(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	dump := `{
	  "public_address": "TCq6atnUUNYJApcxZ6xttv942pyhkDmdTZ",
	  "secret": "plain-tron-private-key"
	}`

	if _, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		Module:          "BNB",
		DefaultCrypto:   "BNB-USDT",
		AccountPassword: "new-account-password",
	}); err == nil || !strings.Contains(err.Error(), "is not valid for BNB") {
		t.Fatalf("expected invalid BNB address error, got %v", err)
	}
}

func TestImportLegacyAccountsJSONReadsTopLevelTRONArrayExport(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	dump := `[
	  {
	    "base58check_address": "TLegacyArrayAddress1111111111111111111",
	    "private_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	    "symbol": "TRC20-USDT",
	    "created_at": "2026-06-04 10:11:12"
	  },
	  {
	    "address": "TLegacyArrayAddress2222222222222222222",
	    "privateKey": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	  }
	]`

	report, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		Module:          "TRON",
		DefaultCrypto:   "USDT",
		AccountPassword: "new-account-password",
	})
	if err != nil {
		t.Fatalf("import legacy tron array: %v", err)
	}
	if report.Rows != 2 || report.Modules["TRON"] != 2 || report.Cryptos["USDT"] != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	accounts, err := store.ListAccounts(ctx, "TRON", "USDT")
	if err != nil {
		t.Fatalf("list imported tron accounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("unexpected imported tron accounts: %+v", accounts)
	}
	got := map[string]string{}
	for _, account := range accounts {
		decrypted, err := decryptSecret("new-account-password", account.PrivateKeyHex)
		if err != nil {
			t.Fatalf("decrypt imported tron secret: %v", err)
		}
		got[account.Address] = decrypted
	}
	if got["TLegacyArrayAddress1111111111111111111"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("first tron secret was not imported: %+v", got)
	}
	if got["TLegacyArrayAddress2222222222222222222"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("second tron secret was not imported: %+v", got)
	}
}

func TestImportLegacyAccountsJSONReadsTRONAddressSecretMapExport(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	dump := `{
	  "module": "TRX",
	  "crypto": "USDT",
	  "accounts": {
	    "TLegacyMapAddress11111111111111111111": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	    "TLegacyMapAddress22222222222222222222": {
	      "secret_key": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	    }
	  }
	}`

	report, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		AccountPassword: "new-account-password",
	})
	if err != nil {
		t.Fatalf("import legacy tron address map: %v", err)
	}
	if report.Rows != 2 || report.Modules["TRON"] != 2 || report.Cryptos["USDT"] != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	accounts, err := store.ListAccounts(ctx, "TRON", "USDT")
	if err != nil {
		t.Fatalf("list imported tron map accounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("unexpected imported tron map accounts: %+v", accounts)
	}
	got := map[string]string{}
	for _, account := range accounts {
		decrypted, err := decryptSecret("new-account-password", account.PrivateKeyHex)
		if err != nil {
			t.Fatalf("decrypt imported tron map secret: %v", err)
		}
		got[account.Address] = decrypted
	}
	if got["TLegacyMapAddress11111111111111111111"] != "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("first tron map secret was not imported: %+v", got)
	}
	if got["TLegacyMapAddress22222222222222222222"] != "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" {
		t.Fatalf("second tron map secret was not imported: %+v", got)
	}
}

func TestImportLegacyAccountsJSONTreatsPlaceholderCryptoAsDefault(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	dump := `{
	  "accounts": [
	    {
	      "public_address": "TCq6atnUUNYJApcxZ6xttv942pyhkDmdTZ",
	      "secret": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	      "crypto": "-"
	    }
	  ]
	}`

	report, err := ImportLegacyAccountsJSON(ctx, store, strings.NewReader(dump), LegacyAccountImportOptions{
		Module:          "TRON",
		DefaultCrypto:   "USDT",
		AccountPassword: "new-account-password",
	})
	if err != nil {
		t.Fatalf("import placeholder crypto: %v", err)
	}
	if report.Rows != 1 || report.Modules["TRON"] != 1 || report.Cryptos["USDT"] != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	accounts, err := store.ListAccounts(ctx, "TRON", "USDT")
	if err != nil {
		t.Fatalf("list imported accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Address != "TCq6atnUUNYJApcxZ6xttv942pyhkDmdTZ" {
		t.Fatalf("placeholder crypto account was not imported as USDT: %+v", accounts)
	}
}

func TestImportLegacyAccountsMariaDBReadsBNBWalletTables(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	token := "Z0FBQUFBQnFJUm1PM3ZzcUZCeWFLVXVscF9aeEY5WWFsUjlpdnRHVjQtdnZtV21pNTdmRVRQcXg2ZlVYSUxKM2daRFVheF9Ba0VhZC14UFFWVHd4SjZSOXVsUFdvdnFrMG9CVTlqSnpPbHB0VEpKZjl2SXN4SUk9"

	for _, stmt := range []string{
		"DROP TABLE IF EXISTS wallets",
		"DROP TABLE IF EXISTS accounts",
		`CREATE TABLE wallets (
			id BIGINT NOT NULL PRIMARY KEY,
			pub_address VARCHAR(70),
			priv_key VARCHAR(300),
			create_time DATETIME,
			status VARCHAR(10),
			type VARCHAR(30)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE accounts (
			id BIGINT NOT NULL PRIMARY KEY,
			address VARCHAR(70),
			crypto VARCHAR(20),
			amount DECIMAL(52,26),
			last_update DATETIME,
			status VARCHAR(10),
			type VARCHAR(30)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		"INSERT INTO wallets (id, pub_address, priv_key, create_time, status, type) VALUES (1, '0x3333333333333333333333333333333333333333', ?, '2026-06-02 15:46:56', 'ok', 'regular')",
		"INSERT INTO accounts (id, address, crypto, amount, last_update, status, type) VALUES (1, '0x3333333333333333333333333333333333333333', 'BNB-USDT', 0, '2026-06-02 15:46:56', 'ok', 'regular')",
	} {
		if strings.Contains(stmt, "?") {
			if _, err := store.db.ExecContext(ctx, stmt, token); err != nil {
				t.Fatalf("prepare legacy table: %v", err)
			}
			continue
		}
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("prepare legacy table: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS wallets")
		_, _ = store.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS accounts")
	})

	report, err := ImportLegacyAccountsMariaDB(ctx, store, os.Getenv("GO_SHKEEPER_TEST_DATABASE_URL"), LegacyAccountImportOptions{
		Module:                "BNB",
		LegacyAccountPassword: "account-password",
		AccountPassword:       "new-account-password",
	})
	if err != nil {
		t.Fatalf("import legacy mariadb accounts: %v", err)
	}
	if report.Rows != 1 || report.Modules["BNB"] != 1 || report.Cryptos["BNB-USDT"] != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	accounts, err := store.ListAccounts(ctx, "BNB", "BNB-USDT")
	if err != nil {
		t.Fatalf("list imported accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Address != "0x3333333333333333333333333333333333333333" {
		t.Fatalf("unexpected imported accounts: %+v", accounts)
	}
	decrypted, err := decryptSecret("new-account-password", accounts[0].PrivateKeyHex)
	if err != nil {
		t.Fatalf("decrypt imported secret: %v", err)
	}
	if decrypted != "0123456789abcdef" {
		t.Fatalf("unexpected imported secret: %s", decrypted)
	}
}
