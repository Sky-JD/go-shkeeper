package app

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
	ListenAddr                    string
	DatabaseURL                   string
	DatabaseDriver                string
	DatabaseDSN                   string
	DatabaseConfigError           string
	SecretKey                     []byte
	SuggestedWalletAPIKey         string
	RequestTimeout                time.Duration
	PayoutRequestTimeout          time.Duration
	NotificationTimeout           time.Duration
	HTTPReadTimeout               time.Duration
	HTTPWriteTimeout              time.Duration
	HTTPIdleTimeout               time.Duration
	DBMaxOpenConns                int
	DBMaxIdleConns                int
	DBConnMaxIdleTime             time.Duration
	DBConnMaxLifetime             time.Duration
	BalanceWorkers                int
	SchedulerEnabled              bool
	MigrateOnStart                bool
	EnsureCurrenciesOnStart       bool
	DisableCryptoWhenLags         bool
	UnconfirmedTXNotification     bool
	NotificationRetries           int
	NotificationTaskDelay         time.Duration
	MinConfirmationBlockForPayout int
	EnablePayoutCallback          bool
	CallbackUserAgent             string
	LogLevel                      slog.Level
	CryptoAllowList               []string
	Fiats                         []string
}

func LoadConfig() Config {
	cfg := Config{
		ListenAddr:                    env("SHKEEPER_LISTEN", ":5000"),
		DatabaseURL:                   firstEnv("MARIADB_DATABASE_URL", "DATABASE_URL"),
		RequestTimeout:                secondsEnv("REQUESTS_TIMEOUT", 10),
		PayoutRequestTimeout:          secondsEnv("PAYOUT_REQUEST_TIMEOUT", 120),
		NotificationTimeout:           secondsEnv("REQUESTS_NOTIFICATION_TIMEOUT", 30),
		HTTPReadTimeout:               secondsEnv("HTTP_READ_TIMEOUT", 10),
		HTTPWriteTimeout:              secondsEnv("HTTP_WRITE_TIMEOUT", 15),
		HTTPIdleTimeout:               secondsEnv("HTTP_IDLE_TIMEOUT", 60),
		BalanceWorkers:                intEnv("BALANCE_QUERY_WORKERS", 8),
		DBConnMaxIdleTime:             secondsEnv("DB_CONN_MAX_IDLE_SECONDS", 180),
		DBConnMaxLifetime:             secondsEnv("DB_CONN_MAX_LIFETIME_SECONDS", 1800),
		SchedulerEnabled:              boolEnv("SCHEDULER_ENABLED", true),
		MigrateOnStart:                boolEnv("SHKEEPER_MIGRATE_ON_START", true),
		EnsureCurrenciesOnStart:       boolEnv("SHKEEPER_ENSURE_CURRENCIES_ON_START", true),
		DisableCryptoWhenLags:         boolEnv("DISABLE_CRYPTO_WHEN_LAGS", false),
		UnconfirmedTXNotification:     boolEnv("UNCONFIRMED_TX_NOTIFICATION", false),
		NotificationRetries:           intEnv("MAX_RETRIES", 7),
		NotificationTaskDelay:         secondsEnv("NOTIFICATION_TASK_DELAY", 60),
		MinConfirmationBlockForPayout: intEnv("MIN_CONFIRMATION_BLOCK_FOR_PAYOUT", 1),
		EnablePayoutCallback:          boolEnv("ENABLE_PAYOUT_CALLBACK", false),
		CallbackUserAgent:             env("SHKEEPER_CALLBACK_USER_AGENT", "SHKeeper-Go-Callback/1.0"),
		SuggestedWalletAPIKey:         env("SUGGESTED_WALLET_APIKEY", randomToken(24)),
		Fiats:                         splitCSV(env("SHKEEPER_FIATS", "USD,EUR,TRY")),
		CryptoAllowList:               splitCSV(os.Getenv("SHKEEPER_CRYPTOS")),
		LogLevel:                      parseLogLevel(env("LOG_LEVEL", "info")),
	}
	cfg.DBMaxOpenConns = intEnv("DB_MAX_OPEN_CONNS", intMax(4, cfg.BalanceWorkers*4))
	cfg.DBMaxIdleConns = intEnv("DB_MAX_IDLE_CONNS", intMax(4, cfg.BalanceWorkers))

	if len(cfg.SecretKey) == 0 {
		secret := env("SECRET_KEY", "")
		if secret == "" {
			secret = randomToken(48)
		}
		cfg.SecretKey = []byte(secret)
	}

	if cfg.DatabaseURL == "" && strings.TrimSpace(os.Getenv("SQLALCHEMY_DATABASE_URI")) != "" {
		cfg.DatabaseConfigError = "SQLALCHEMY_DATABASE_URI is a legacy Python setting and is not used by go-shkeeper; set MARIADB_DATABASE_URL or DATABASE_URL to mariadb://user:password@host:3306/database"
	}
	if cfg.DatabaseURL == "" {
		cfg.DatabaseURL = "mariadb://shkeeper:shkeeper@127.0.0.1:3306/shkeeper"
	}
	if cfg.DatabaseConfigError == "" {
		cfg.DatabaseConfigError = databaseURLConfigError(cfg.DatabaseURL)
	}
	cfg.DatabaseDriver, cfg.DatabaseDSN = parseDatabaseURL(cfg.DatabaseURL)
	return cfg
}

func parseDatabaseURL(raw string) (string, string) {
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
			return "mysql", user + ":" + pass + "@tcp(" + host + ")/" + dbname + "?" + query.Encode()
		}
	}
	if strings.Contains(raw, "@tcp(") {
		return "mysql", raw
	}
	return "mysql", raw
}

func isDatabaseURL(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(raw, "mysql://") || strings.HasPrefix(raw, "mysql+") || strings.HasPrefix(raw, "mariadb://")
}

func databaseURLConfigError(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(value, "sqlite:") || strings.Contains(value, "sqlite://") || strings.Contains(value, ".sqlite") || strings.Contains(value, "sqlite3") {
		return "SQLite is not supported by go-shkeeper; set MARIADB_DATABASE_URL or DATABASE_URL to mariadb://user:password@host:3306/database"
	}
	if isPostgresDatabaseURL(value) {
		return "PostgreSQL/PGDB is not supported by go-shkeeper; use MariaDB so the Go runtime can keep the legacy SHKeeper table/index contract"
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

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func boolEnv(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return !(v == "0" || v == "false" || v == "no" || v == "off")
}

func intEnv(key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return v
}

func secondsEnv(key string, fallback int) time.Duration {
	return time.Duration(intEnv(key, fallback)) * time.Second
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
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
