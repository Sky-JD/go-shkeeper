package app

import (
	"strings"
	"testing"
)

func TestDefaultDatabaseURLIsMariaDB(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SQLALCHEMY_DATABASE_URI", "")

	cfg := LoadConfig()
	if cfg.DatabaseURL != "mariadb://shkeeper:shkeeper@127.0.0.1:3306/shkeeper" {
		t.Fatalf("unexpected default database URL: %s", cfg.DatabaseURL)
	}
	if cfg.DatabaseDriver != "mysql" {
		t.Fatalf("MariaDB should use mysql driver, got %s", cfg.DatabaseDriver)
	}
	if !cfg.MigrateOnStart || !cfg.EnsureCurrenciesOnStart {
		t.Fatalf("startup migration and currency registration should default on")
	}
}

func TestMariaDBDatabaseURLTakesPrecedence(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "mariadb://maria:pass@db:3306/main")
	t.Setenv("DATABASE_URL", "mariadb://generic:pass@db:3306/other")

	cfg := LoadConfig()
	if cfg.DatabaseURL != "mariadb://maria:pass@db:3306/main" {
		t.Fatalf("MARIADB_DATABASE_URL should win, got %s", cfg.DatabaseURL)
	}
	if cfg.DatabaseConfigError != "" {
		t.Fatalf("unexpected database config error: %s", cfg.DatabaseConfigError)
	}
}

func TestMariaDBDatabaseURLOverridesLegacySQLiteEnv(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "mariadb://maria:pass@db:3306/main")
	t.Setenv("DATABASE_URL", "sqlite:////data/shkeeper.sqlite")
	t.Setenv("SQLALCHEMY_DATABASE_URI", "sqlite:////data/shkeeper.sqlite")

	cfg := LoadConfig()
	if cfg.DatabaseConfigError != "" {
		t.Fatalf("explicit MariaDB config should override legacy SQLite env, got %s", cfg.DatabaseConfigError)
	}
	if cfg.DatabaseURL != "mariadb://maria:pass@db:3306/main" {
		t.Fatalf("MARIADB_DATABASE_URL should win, got %s", cfg.DatabaseURL)
	}
	if strings.Contains(strings.ToLower(cfg.DatabaseDSN), "sqlite") {
		t.Fatalf("MariaDB DSN must not include SQLite details: %s", cfg.DatabaseDSN)
	}
}

func TestParseMariaDBURL(t *testing.T) {
	driver, dsn := parseDatabaseURL("mariadb://user:pass@db:3306/shkeeper")
	if driver != "mysql" {
		t.Fatalf("unexpected driver: %s", driver)
	}
	if dsn != "user:pass@tcp(db:3306)/shkeeper?charset=utf8mb4&loc=Local&parseTime=true" {
		t.Fatalf("unexpected dsn: %s", dsn)
	}
}

func TestSQLiteDatabaseURLIsRejected(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "sqlite:////data/shkeeper.sqlite")
	t.Setenv("SQLALCHEMY_DATABASE_URI", "")

	cfg := LoadConfig()
	if cfg.DatabaseConfigError == "" {
		t.Fatalf("expected SQLite config to be rejected")
	}
}

func TestPostgresDatabaseURLIsRejected(t *testing.T) {
	for _, raw := range []string{
		"postgres://user:pass@pgdb:5432/shkeeper",
		"postgresql://user:pass@pgdb:5432/shkeeper",
		"pgdb://user:pass@pgdb:5432/shkeeper",
	} {
		t.Setenv("MARIADB_DATABASE_URL", "")
		t.Setenv("DATABASE_URL", raw)
		t.Setenv("SQLALCHEMY_DATABASE_URI", "")

		cfg := LoadConfig()
		if cfg.DatabaseConfigError == "" {
			t.Fatalf("expected PostgreSQL/PGDB config to be rejected for %s", raw)
		}
		if !strings.Contains(cfg.DatabaseConfigError, "PostgreSQL/PGDB") {
			t.Fatalf("expected PostgreSQL-specific error for %s, got %s", raw, cfg.DatabaseConfigError)
		}
	}
}

func TestLegacySQLAlchemyDatabaseURLIsRejected(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SQLALCHEMY_DATABASE_URI", "sqlite:////data/shkeeper.sqlite")

	cfg := LoadConfig()
	if cfg.DatabaseConfigError == "" {
		t.Fatalf("expected legacy SQLAlchemy database config to be rejected")
	}
	if !strings.Contains(cfg.DatabaseConfigError, "SQLALCHEMY_DATABASE_URI") {
		t.Fatalf("expected SQLAlchemy-specific error, got %s", cfg.DatabaseConfigError)
	}
	if strings.Contains(strings.ToLower(cfg.DatabaseURL), "sqlite") || strings.Contains(strings.ToLower(cfg.DatabaseDSN), "sqlite") {
		t.Fatalf("legacy SQLite URL must not be adopted: url=%s dsn=%s", cfg.DatabaseURL, cfg.DatabaseDSN)
	}
	if cfg.DatabaseURL != "mariadb://shkeeper:shkeeper@127.0.0.1:3306/shkeeper" {
		t.Fatalf("legacy config should fall back to MariaDB placeholder, got %s", cfg.DatabaseURL)
	}
}

func TestLoadConfigCanDisableStartupWritesForReadOnlyVerification(t *testing.T) {
	t.Setenv("SHKEEPER_MIGRATE_ON_START", "false")
	t.Setenv("SHKEEPER_ENSURE_CURRENCIES_ON_START", "false")

	cfg := LoadConfig()
	if cfg.MigrateOnStart || cfg.EnsureCurrenciesOnStart {
		t.Fatalf("startup write guards not disabled: migrate=%t ensure=%t", cfg.MigrateOnStart, cfg.EnsureCurrenciesOnStart)
	}
}

func TestLoadConfigDatabasePoolSettings(t *testing.T) {
	t.Setenv("MARIADB_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SQLALCHEMY_DATABASE_URI", "")
	t.Setenv("BALANCE_QUERY_WORKERS", "3")
	t.Setenv("DB_CONN_MAX_IDLE_SECONDS", "11")
	t.Setenv("DB_CONN_MAX_LIFETIME_SECONDS", "22")

	cfg := LoadConfig()
	if cfg.DBMaxOpenConns != 12 {
		t.Fatalf("unexpected derived max open conns: %d", cfg.DBMaxOpenConns)
	}
	if cfg.DBMaxIdleConns != 4 {
		t.Fatalf("unexpected derived max idle conns: %d", cfg.DBMaxIdleConns)
	}
	if cfg.DBConnMaxIdleTime.String() != "11s" || cfg.DBConnMaxLifetime.String() != "22s" {
		t.Fatalf("unexpected db connection durations: idle=%s lifetime=%s", cfg.DBConnMaxIdleTime, cfg.DBConnMaxLifetime)
	}

	t.Setenv("DB_MAX_OPEN_CONNS", "64")
	t.Setenv("DB_MAX_IDLE_CONNS", "16")
	cfg = LoadConfig()
	if cfg.DBMaxOpenConns != 64 || cfg.DBMaxIdleConns != 16 {
		t.Fatalf("explicit db pool settings were not honored: open=%d idle=%d", cfg.DBMaxOpenConns, cfg.DBMaxIdleConns)
	}
}

func TestCryptoAllowListHonorsExplicitWalletDisabled(t *testing.T) {
	t.Setenv("ETH_WALLET", "disabled")
	reg := &CryptoRegistry{cfg: Config{CryptoAllowList: []string{"BTC", "ETH"}}}
	if !reg.enabledByConfig(CryptoModule{Name: "BTC", DefaultOn: true}) {
		t.Fatalf("allow-listed BTC should be enabled")
	}
	if reg.enabledByConfig(CryptoModule{Name: "ETH"}) {
		t.Fatalf("explicit ETH_WALLET=disabled must override SHKEEPER_CRYPTOS")
	}
}
