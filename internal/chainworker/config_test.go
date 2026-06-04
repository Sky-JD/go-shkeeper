package chainworker

import (
	"strings"
	"testing"
)

func TestLoadConfigMariaDBDatabaseURLTakesPrecedence(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "mariadb://maria:pass@mariadb:3306/shkeeper")
	t.Setenv("DATABASE_URL", "mariadb://generic:pass@mariadb:3306/other")

	cfg := LoadConfig()
	if cfg.DatabaseURL != "mariadb://maria:pass@mariadb:3306/shkeeper" {
		t.Fatalf("MARIADB_DATABASE_URL should win, got %s", cfg.DatabaseURL)
	}
	if cfg.DatabaseError != "" {
		t.Fatalf("unexpected database config error: %s", cfg.DatabaseError)
	}
}

func TestLoadConfigMariaDBURLOverridesLegacySQLiteEnv(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "mariadb://maria:pass@mariadb:3306/shkeeper")
	t.Setenv("DATABASE_URL", "sqlite:////data/shkeeper.sqlite")

	cfg := LoadConfig()
	if cfg.DatabaseError != "" {
		t.Fatalf("explicit MariaDB config should override legacy SQLite env, got %s", cfg.DatabaseError)
	}
	if cfg.DatabaseURL != "mariadb://maria:pass@mariadb:3306/shkeeper" {
		t.Fatalf("MARIADB_DATABASE_URL should win, got %s", cfg.DatabaseURL)
	}
	if strings.Contains(strings.ToLower(cfg.DatabaseDSN), "sqlite") {
		t.Fatalf("MariaDB DSN must not include SQLite details: %s", cfg.DatabaseDSN)
	}
}

func TestLoadConfigMoneroAliases(t *testing.T) {
	t.Setenv("CHAIN_MODULE", "XMR")
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "mariadb://user:pass@mariadb:3306/shkeeper")
	t.Setenv("MONERO_DAEMON_URL", "http://monerod:18081")
	t.Setenv("MONERO_WALLET_RPC_URL", "http://wallet:18083")
	t.Setenv("MONERO_DAEMON_USER", "daemon-user")
	t.Setenv("MONERO_DAEMON_PASS", "daemon-pass")
	t.Setenv("MONERO_WALLET_RPC_USER", "wallet-user")
	t.Setenv("MONERO_WALLET_RPC_PASS", "wallet-pass")
	t.Setenv("MONERO_USERNAME", "worker-user")
	t.Setenv("MONERO_PASSWORD", "worker-pass")

	cfg := LoadConfig()
	if cfg.Module != "XMR" {
		t.Fatalf("unexpected module: %s", cfg.Module)
	}
	if cfg.FullnodeURL != "http://monerod:18081" || cfg.WalletRPCURL != "http://wallet:18083" {
		t.Fatalf("unexpected monero urls: daemon=%s wallet=%s", cfg.FullnodeURL, cfg.WalletRPCURL)
	}
	if cfg.RPCUsername != "daemon-user" || cfg.RPCPassword != "daemon-pass" {
		t.Fatalf("unexpected daemon auth: %s/%s", cfg.RPCUsername, cfg.RPCPassword)
	}
	if cfg.WalletRPCUser != "wallet-user" || cfg.WalletRPCPass != "wallet-pass" {
		t.Fatalf("unexpected wallet auth: %s/%s", cfg.WalletRPCUser, cfg.WalletRPCPass)
	}
	if cfg.Username != "worker-user" || cfg.Password != "worker-pass" {
		t.Fatalf("unexpected worker auth: %s/%s", cfg.Username, cfg.Password)
	}
}

func TestLoadConfigLightningAliases(t *testing.T) {
	t.Setenv("CHAIN_MODULE", "BTC-LIGHTNING")
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "mariadb://user:pass@mariadb:3306/shkeeper")
	t.Setenv("LND_REST_URL", "https://lnd.example:8080")
	t.Setenv("BTC_LIGHTNING_USERNAME", "ln-user")
	t.Setenv("BTC_LIGHTNING_PASSWORD", "ln-pass")

	cfg := LoadConfig()
	if cfg.Module != "BTC-LIGHTNING" {
		t.Fatalf("unexpected module: %s", cfg.Module)
	}
	if cfg.FullnodeURL != "https://lnd.example:8080" {
		t.Fatalf("unexpected lnd url: %s", cfg.FullnodeURL)
	}
	if cfg.Username != "ln-user" || cfg.Password != "ln-pass" {
		t.Fatalf("unexpected lightning auth: %s/%s", cfg.Username, cfg.Password)
	}
}

func TestLoadConfigSolanaAliases(t *testing.T) {
	t.Setenv("CHAIN_MODULE", "SOL")
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "mariadb://user:pass@mariadb:3306/shkeeper")
	t.Setenv("SOLANA_RPC_URL", "https://solana.example")
	t.Setenv("SOLANA_USERNAME", "sol-user")
	t.Setenv("SOLANA_PASSWORD", "sol-pass")
	t.Setenv("SOLANA_RPC_USERNAME", "rpc-user")
	t.Setenv("SOLANA_RPC_PASSWORD", "rpc-pass")

	cfg := LoadConfig()
	if cfg.Module != "SOL" {
		t.Fatalf("unexpected module: %s", cfg.Module)
	}
	if cfg.FullnodeURL != "https://solana.example" {
		t.Fatalf("unexpected solana url: %s", cfg.FullnodeURL)
	}
	if cfg.Username != "sol-user" || cfg.Password != "sol-pass" {
		t.Fatalf("unexpected solana worker auth: %s/%s", cfg.Username, cfg.Password)
	}
	if cfg.RPCUsername != "rpc-user" || cfg.RPCPassword != "rpc-pass" {
		t.Fatalf("unexpected solana rpc auth: %s/%s", cfg.RPCUsername, cfg.RPCPassword)
	}
}

func TestLoadConfigBitcoinLikeAliases(t *testing.T) {
	t.Setenv("CHAIN_MODULE", "LTC")
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "mariadb://user:pass@mariadb:3306/shkeeper")
	t.Setenv("LTC_RPC_URL", "http://litecoin-node:9332")
	t.Setenv("LTC_RPC_USERNAME", "ltc-rpc-user")
	t.Setenv("LTC_RPC_PASSWORD", "ltc-rpc-pass")
	t.Setenv("LTC_USERNAME", "ltc-worker-user")
	t.Setenv("LTC_PASSWORD", "ltc-worker-pass")

	cfg := LoadConfig()
	if cfg.FullnodeURL != "http://litecoin-node:9332" {
		t.Fatalf("unexpected litecoin rpc url: %s", cfg.FullnodeURL)
	}
	if cfg.RPCUsername != "ltc-rpc-user" || cfg.RPCPassword != "ltc-rpc-pass" {
		t.Fatalf("unexpected litecoin rpc auth: %s/%s", cfg.RPCUsername, cfg.RPCPassword)
	}
	if cfg.Username != "ltc-worker-user" || cfg.Password != "ltc-worker-pass" {
		t.Fatalf("unexpected litecoin worker auth: %s/%s", cfg.Username, cfg.Password)
	}
}

func TestLoadConfigDatabasePoolSettings(t *testing.T) {
	t.Setenv("CHAIN_MODULE", "BNB")
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "mariadb://user:pass@mariadb:3306/shkeeper")
	t.Setenv("DB_MAX_OPEN_CONNS", "32")
	t.Setenv("DB_MAX_IDLE_CONNS", "12")
	t.Setenv("DB_CONN_MAX_IDLE_SECONDS", "9")
	t.Setenv("DB_CONN_MAX_LIFETIME_SECONDS", "19")

	cfg := LoadConfig()
	if cfg.DBMaxOpenConns != 32 || cfg.DBMaxIdleConns != 12 {
		t.Fatalf("unexpected db pool sizes: open=%d idle=%d", cfg.DBMaxOpenConns, cfg.DBMaxIdleConns)
	}
	if cfg.DBConnMaxIdleTime.String() != "9s" || cfg.DBConnMaxLifetime.String() != "19s" {
		t.Fatalf("unexpected db connection durations: idle=%s lifetime=%s", cfg.DBConnMaxIdleTime, cfg.DBConnMaxLifetime)
	}
}

func TestLoadConfigEVMWorkerAuthAliases(t *testing.T) {
	tests := []struct {
		name      string
		module    string
		userEnv   string
		passEnv   string
		wantUser  string
		wantPass  string
		fallback  string
		fallbackP string
	}{
		{
			name:      "polygon main service names",
			module:    "MATIC",
			userEnv:   "POLYGON_USERNAME",
			passEnv:   "POLYGON_PASSWORD",
			wantUser:  "polygon-user",
			wantPass:  "polygon-pass",
			fallback:  "matic-user",
			fallbackP: "matic-pass",
		},
		{
			name:      "avalanche main service names",
			module:    "AVAX",
			userEnv:   "AVALANCHE_USERNAME",
			passEnv:   "AVALANCHE_PASSWORD",
			wantUser:  "avalanche-user",
			wantPass:  "avalanche-pass",
			fallback:  "avax-user",
			fallbackP: "avax-pass",
		},
		{
			name:      "arbitrum main service names",
			module:    "ARBETH",
			userEnv:   "ARB_USERNAME",
			passEnv:   "ARB_PASSWORD",
			wantUser:  "arb-user",
			wantPass:  "arb-pass",
			fallback:  "arbeth-user",
			fallbackP: "arbeth-pass",
		},
		{
			name:      "optimism main service names",
			module:    "OPETH",
			userEnv:   "OP_USERNAME",
			passEnv:   "OP_PASSWORD",
			wantUser:  "op-user",
			wantPass:  "op-pass",
			fallback:  "opeth-user",
			fallbackP: "opeth-pass",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CHAIN_MODULE", tt.module)
			t.Setenv("MARIADB_DATABASE_URL", "")
			t.Setenv("DATABASE_URL", "mariadb://user:pass@mariadb:3306/shkeeper")
			t.Setenv(tt.userEnv, tt.wantUser)
			t.Setenv(tt.passEnv, tt.wantPass)
			t.Setenv(tt.module+"_USERNAME", tt.fallback)
			t.Setenv(tt.module+"_PASSWORD", tt.fallbackP)

			cfg := LoadConfig()
			if cfg.Username != tt.wantUser || cfg.Password != tt.wantPass {
				t.Fatalf("expected alias auth %s/%s, got %s/%s", tt.wantUser, tt.wantPass, cfg.Username, cfg.Password)
			}
		})
	}
}

func TestLoadConfigEVMWorkerAuthModuleFallback(t *testing.T) {
	t.Setenv("CHAIN_MODULE", "MATIC")
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "mariadb://user:pass@mariadb:3306/shkeeper")
	t.Setenv("MATIC_USERNAME", "matic-user")
	t.Setenv("MATIC_PASSWORD", "matic-pass")

	cfg := LoadConfig()
	if cfg.Username != "matic-user" || cfg.Password != "matic-pass" {
		t.Fatalf("expected module fallback auth, got %s/%s", cfg.Username, cfg.Password)
	}
}

func TestLoadConfigRejectsSQLiteDatabaseURL(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "sqlite:////data/shkeeper.sqlite")

	cfg := LoadConfig()
	if cfg.DatabaseError == "" {
		t.Fatalf("expected SQLite config to be rejected")
	}
}

func TestLoadConfigRejectsPostgresDatabaseURL(t *testing.T) {
	for _, raw := range []string{
		"postgres://user:pass@pgdb:5432/shkeeper",
		"postgresql://user:pass@pgdb:5432/shkeeper",
		"pgdb://user:pass@pgdb:5432/shkeeper",
	} {
		t.Setenv("MARIADB_DATABASE_URL", "")
		t.Setenv("DATABASE_URL", raw)

		cfg := LoadConfig()
		if cfg.DatabaseError == "" {
			t.Fatalf("expected PostgreSQL/PGDB config to be rejected for %s", raw)
		}
		if !strings.Contains(cfg.DatabaseError, "PostgreSQL/PGDB") {
			t.Fatalf("expected PostgreSQL-specific error for %s, got %s", raw, cfg.DatabaseError)
		}
	}
}
