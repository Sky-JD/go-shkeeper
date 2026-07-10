package chainworker

import (
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Module                       string
	ListenAddr                   string
	DatabaseURL                  string
	DatabaseDSN                  string
	DatabaseError                string
	FullnodeURL                  string
	FullnodeURLs                 []string
	WalletRPCURL                 string
	EVMChainID                   int64
	RPCUsername                  string
	RPCPassword                  string
	WalletRPCUser                string
	WalletRPCPass                string
	Username                     string
	Password                     string
	AccountPassword              string
	BackendKey                   string
	RequestTimeout               time.Duration
	DBMaxOpenConns               int
	DBMaxIdleConns               int
	DBConnMaxIdleTime            time.Duration
	DBConnMaxLifetime            time.Duration
	DepositScanEnabled           bool
	DepositScanInterval          time.Duration
	DepositScanReconcileInterval time.Duration
	DepositScanMaxInvoiceAge     time.Duration
	DepositScanBatchSize         int
	DepositScanBlockStep         int64
	DepositScanAddressTopicBatch int
	DepositScanMinConfirmations  int64
	DepositScanStartMargin       time.Duration
	DepositDispatchInterval      time.Duration
	DepositDispatchBatchSize     int
	DepositDispatchConcurrency   int
	DepositEventMaxAttempts      int
	EVMAverageBlockSeconds       int64
	ShkeeperAPIBaseURL           string
	LogLevel                     slog.Level
}

func LoadConfig() Config {
	module := strings.ToUpper(env("CHAIN_MODULE", "BNB"))
	cfg := Config{
		Module:                       module,
		ListenAddr:                   env("CHAIN_WORKER_LISTEN", ":6000"),
		DatabaseURL:                  firstNonEmpty(os.Getenv("MARIADB_DATABASE_URL"), os.Getenv("DATABASE_URL"), "mariadb://shkeeper:shkeeper@mariadb:3306/shkeeper"),
		FullnodeURL:                  defaultFullnode(module),
		WalletRPCURL:                 env("WALLET_RPC_URL", defaultWalletRPC(module)),
		EVMChainID:                   int64Env("EVM_CHAIN_ID", defaultEVMChainID(module)),
		RPCUsername:                  firstEnv("RPC_USERNAME", module+"_RPC_USERNAME", module+"_NODE_USERNAME"),
		RPCPassword:                  firstEnv("RPC_PASSWORD", module+"_RPC_PASSWORD", module+"_NODE_PASSWORD"),
		WalletRPCUser:                firstEnv("WALLET_RPC_USERNAME", module+"_WALLET_RPC_USERNAME"),
		WalletRPCPass:                firstEnv("WALLET_RPC_PASSWORD", module+"_WALLET_RPC_PASSWORD"),
		Username:                     firstNonEmpty(firstEnv(workerAuthEnvKeys(module, "USERNAME")...), strings.ToLower(module)),
		Password:                     firstNonEmpty(firstEnv(workerAuthEnvKeys(module, "PASSWORD")...), randomToken(24)),
		AccountPassword:              env("ACCOUNT_PASSWORD", randomToken(32)),
		BackendKey:                   env("SHKEEPER_BACKEND_KEY", randomToken(32)),
		RequestTimeout:               secondsEnv("REQUESTS_TIMEOUT", 20),
		DBMaxOpenConns:               intEnv("DB_MAX_OPEN_CONNS", 16),
		DBMaxIdleConns:               intEnv("DB_MAX_IDLE_CONNS", 8),
		DBConnMaxIdleTime:            secondsEnv("DB_CONN_MAX_IDLE_SECONDS", 180),
		DBConnMaxLifetime:            secondsEnv("DB_CONN_MAX_LIFETIME_SECONDS", 1800),
		DepositScanEnabled:           boolEnv("EVM_DEPOSIT_SCAN_ENABLED", true),
		DepositScanInterval:          secondsEnv("EVM_DEPOSIT_SCAN_INTERVAL_SECONDS", 20),
		DepositScanReconcileInterval: secondsEnv("EVM_DEPOSIT_RECONCILE_INTERVAL_SECONDS", 600),
		DepositScanMaxInvoiceAge:     secondsEnv("EVM_DEPOSIT_SCAN_MAX_INVOICE_AGE_SECONDS", 7200),
		DepositScanBatchSize:         intEnv("EVM_DEPOSIT_SCAN_BATCH_SIZE", 200000),
		DepositScanBlockStep:         int64Env("EVM_DEPOSIT_SCAN_BLOCK_STEP", 500),
		DepositScanAddressTopicBatch: intEnv("EVM_DEPOSIT_SCAN_ADDRESS_BATCH_SIZE", 100),
		DepositScanMinConfirmations:  int64Env("EVM_DEPOSIT_SCAN_MIN_CONFIRMATIONS", 2),
		DepositScanStartMargin:       secondsEnv("EVM_DEPOSIT_SCAN_START_MARGIN_SECONDS", 600),
		DepositDispatchInterval:      secondsEnv("EVM_DEPOSIT_DISPATCH_INTERVAL_SECONDS", 2),
		DepositDispatchBatchSize:     intEnv("EVM_DEPOSIT_DISPATCH_BATCH_SIZE", 200),
		DepositDispatchConcurrency:   intEnv("EVM_DEPOSIT_DISPATCH_CONCURRENCY", 8),
		DepositEventMaxAttempts:      intEnv("EVM_DEPOSIT_EVENT_MAX_ATTEMPTS", 20),
		EVMAverageBlockSeconds:       int64Env("EVM_AVERAGE_BLOCK_SECONDS", defaultEVMAverageBlockSeconds(module)),
		ShkeeperAPIBaseURL:           normalizeShkeeperAPIBaseURL(),
		LogLevel:                     parseLogLevel(env("LOG_LEVEL", "info")),
	}
	if v := os.Getenv("FULLNODE_URL"); v != "" {
		cfg.FullnodeURL = v
	}
	if v := firstNonEmpty(os.Getenv(module+"_RPC_URL"), os.Getenv(module+"_FULLNODE_URL")); v != "" && os.Getenv("FULLNODE_URL") == "" {
		cfg.FullnodeURL = v
	}
	if v := os.Getenv("LND_REST_URL"); v != "" && module == "BTC-LIGHTNING" {
		cfg.FullnodeURL = v
	}
	if v := os.Getenv("MONERO_DAEMON_URL"); v != "" && module == "XMR" {
		cfg.FullnodeURL = v
	}
	if v := os.Getenv("MONERO_WALLET_RPC_URL"); v != "" && module == "XMR" {
		cfg.WalletRPCURL = v
	}
	if module == "XMR" {
		cfg.RPCUsername = firstNonEmpty(cfg.RPCUsername, os.Getenv("MONERO_DAEMON_USER"))
		cfg.RPCPassword = firstNonEmpty(cfg.RPCPassword, os.Getenv("MONERO_DAEMON_PASS"))
		cfg.WalletRPCUser = firstNonEmpty(cfg.WalletRPCUser, os.Getenv("MONERO_WALLET_RPC_USER"))
		cfg.WalletRPCPass = firstNonEmpty(cfg.WalletRPCPass, os.Getenv("MONERO_WALLET_RPC_PASS"))
		cfg.Username = firstNonEmpty(os.Getenv("MONERO_USERNAME"), cfg.Username)
		cfg.Password = firstNonEmpty(os.Getenv("MONERO_PASSWORD"), cfg.Password)
	}
	if module == "BTC-LIGHTNING" {
		cfg.Username = firstNonEmpty(os.Getenv("BTC_LIGHTNING_USERNAME"), cfg.Username)
		cfg.Password = firstNonEmpty(os.Getenv("BTC_LIGHTNING_PASSWORD"), cfg.Password)
	}
	if module == "SOL" {
		cfg.FullnodeURL = firstNonEmpty(os.Getenv("SOLANA_FULLNODE_URL"), os.Getenv("SOLANA_RPC_URL"), cfg.FullnodeURL)
		cfg.Username = firstNonEmpty(os.Getenv("SOLANA_USERNAME"), cfg.Username)
		cfg.Password = firstNonEmpty(os.Getenv("SOLANA_PASSWORD"), cfg.Password)
		cfg.RPCUsername = firstNonEmpty(cfg.RPCUsername, os.Getenv("SOLANA_RPC_USERNAME"))
		cfg.RPCPassword = firstNonEmpty(cfg.RPCPassword, os.Getenv("SOLANA_RPC_PASSWORD"))
	}
	cfg.FullnodeURLs = splitCSV(firstEnv(module+"_FULLNODE_URLS", "FULLNODE_URLS"))
	if len(cfg.FullnodeURLs) > 0 && os.Getenv("FULLNODE_URL") == "" {
		cfg.FullnodeURL = cfg.FullnodeURLs[0]
	}
	if len(cfg.FullnodeURLs) == 0 {
		cfg.FullnodeURLs = []string{cfg.FullnodeURL}
	}
	cfg.DatabaseError = databaseURLConfigError(cfg.DatabaseURL)
	cfg.DatabaseDSN = parseDatabaseURL(cfg.DatabaseURL)
	return cfg
}

func parseDatabaseURL(raw string) string {
	if isDatabaseURL(raw) {
		u, err := url.Parse(raw)
		if err == nil {
			user := u.User.Username()
			pass, _ := u.User.Password()
			host := u.Host
			dbname := strings.TrimPrefix(u.Path, "/")
			query := u.Query()
			if query.Get("parseTime") == "" {
				query.Set("parseTime", "true")
			}
			if query.Get("charset") == "" {
				query.Set("charset", "utf8mb4")
			}
			if query.Get("loc") == "" {
				query.Set("loc", "Local")
			}
			return user + ":" + pass + "@tcp(" + host + ")/" + dbname + "?" + query.Encode()
		}
	}
	return raw
}

func isDatabaseURL(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(raw, "mysql://") || strings.HasPrefix(raw, "mysql+") || strings.HasPrefix(raw, "mariadb://")
}

func databaseURLConfigError(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(value, "sqlite:") || strings.Contains(value, "sqlite://") || strings.Contains(value, ".sqlite") || strings.Contains(value, "sqlite3") {
		return "SQLite is not supported by chain-worker; set MARIADB_DATABASE_URL or DATABASE_URL to mariadb://user:password@host:3306/database"
	}
	if isPostgresDatabaseURL(value) {
		return "PostgreSQL/PGDB is not supported by chain-worker; use MariaDB so worker account/task storage matches the Go main service"
	}
	if isDatabaseURL(raw) || strings.Contains(raw, "@tcp(") {
		return ""
	}
	return "MARIADB_DATABASE_URL or DATABASE_URL must be a MariaDB/MySQL DSN, for example mariadb://user:password@host:3306/database"
}

func isPostgresDatabaseURL(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") || strings.HasPrefix(raw, "pgdb://")
}

func defaultFullnode(module string) string {
	switch strings.ToUpper(module) {
	case "TRON":
		return "https://api.trongrid.io"
	case "BTC":
		return "http://bitcoind:8332"
	case "LTC":
		return "http://litecoind:9332"
	case "DOGE":
		return "http://dogecoind:22555"
	case "FIRO":
		return "http://firod:8332"
	case "BTC-LIGHTNING":
		return "https://lnd:8080"
	case "XMR":
		return "http://monerod:1111/json_rpc"
	case "XRP":
		return "https://s1.ripple.com:51234"
	case "SOL":
		return "https://api.mainnet-beta.solana.com"
	case "ETH":
		return "https://ethereum-rpc.publicnode.com"
	case "MATIC":
		return "https://polygon-bor-rpc.publicnode.com"
	case "AVAX":
		return "https://avalanche-c-chain-rpc.publicnode.com"
	case "ARBETH":
		return "https://arbitrum-one-rpc.publicnode.com"
	case "OPETH":
		return "https://optimism-rpc.publicnode.com"
	default:
		return "https://bsc.rpc.blxrbdn.com"
	}
}

func defaultWalletRPC(module string) string {
	switch strings.ToUpper(module) {
	case "XMR":
		return "http://monero-wallet-rpc:2222/json_rpc"
	default:
		return ""
	}
}

func env(key, fallback string) string {
	return getenv(key, fallback)
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func workerAuthEnvKeys(module string, suffix string) []string {
	module = strings.ToUpper(module)
	keys := make([]string, 0, 3)
	switch module {
	case "MATIC":
		keys = append(keys, "POLYGON_"+suffix)
	case "AVAX":
		keys = append(keys, "AVALANCHE_"+suffix)
	case "ARBETH":
		keys = append(keys, "ARB_"+suffix)
	case "OPETH":
		keys = append(keys, "OP_"+suffix)
	}
	keys = append(keys, module+"_"+suffix)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func secondsEnv(key string, fallback int) time.Duration {
	return time.Duration(intEnv(key, fallback)) * time.Second
}

func intEnv(key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return v
}

func int64Env(key string, fallback int64) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

func defaultEVMChainID(module string) int64 {
	switch strings.ToUpper(module) {
	case "BNB":
		return 56
	case "MATIC":
		return 137
	case "AVAX":
		return 43114
	case "ARBETH":
		return 42161
	case "OPETH":
		return 10
	default:
		return 1
	}
}

func defaultEVMAverageBlockSeconds(module string) int64 {
	switch strings.ToUpper(module) {
	case "ETH":
		return 12
	case "BNB":
		return 1
	case "MATIC", "AVAX", "ARBETH", "OPETH":
		return 2
	default:
		return 3
	}
}

func normalizeShkeeperAPIBaseURL() string {
	if value := strings.TrimSpace(os.Getenv("SHKEEPER_API_BASE_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := strings.TrimSpace(os.Getenv("SHKEEPER_BASE_URL")); value != "" {
		value = strings.TrimRight(value, "/")
		if strings.HasSuffix(value, "/api/v1") {
			return value
		}
		return value + "/api/v1"
	}
	return "http://go-shkeeper:5000/api/v1"
}

func randomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "change-me"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
