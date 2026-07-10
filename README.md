# Go SHKeeper

[English](README.md) | [简体中文](README.zh-CN.md)

Go SHKeeper is a Go rewrite of the SHKeeper application layer. It keeps the legacy table names and public API surface where practical, while adding a Vue admin UI, a complete order query API, Go-native chain workers, and MariaDB-only deployment tooling.

## Acknowledgements

This Go rewrite was developed with OpenAI Codex under the maintainer's direction and review. Thanks to Codex for the implementation, migration analysis, deployment diagnostics, verification work, and documentation support across the project.

## Highlights

- Go application service with MariaDB/MySQL storage through `database/sql`.
- Vue/Vite admin UI under `web/admin`, embedded into the Go binary from `internal/app/admin_dist`.
- Admin password setup, login, account update, and TOTP 2FA.
- MariaDB-backed wallet, invoice, transaction, payout, exchange-rate, notification, and order-index tables.
- Complete order lookup through `/api/v1/orders` and `/api/v1/orders/{external_id}`.
- Compatibility endpoints for existing SHKeeper integrations, including payment requests, payouts, wallet notifications, status checks, exchange rates, and metrics.
- Modular Go chain workers for BTC, LTC, DOGE, FIRO, BTC Lightning, TRON, BNB, EVM networks, Solana, Monero, and XRP.
- Go-native migration and verification commands such as `/app/import-legacy-main-mariadb`, `/app/import-legacy-accounts`, `/app/deploy-check`, `/app/cutover-preflight`, `/app/release-audit`, `/app/runtime-audit`, and `/app/goal-audit`.
- No SQLite runtime fallback. The Go service accepts MariaDB/MySQL DSNs and rejects SQLite/PostgreSQL URLs.

## Quick Start

The fastest first run uses Docker Compose with a small MariaDB-backed stack. It starts only the main application and database, which is enough to open the admin UI, initialize schema, inspect health endpoints, and begin configuration.

### 1. Install Prerequisites

- Git
- Docker with Docker Compose

Optional tools for source development:

- Go 1.24 or newer
- Node.js 20 or newer

### 2. Clone The Repository

```bash
git clone https://github.com/Sky-JD/go-shkeeper.git
cd go-shkeeper
```

### 3. Bootstrap With The Install Script

Recommended for the first run:

```bash
bash deploy/install.sh
```

The installer will:

- create `.env` if it does not exist
- detect missing or placeholder values such as `change-root-password`
- prompt you to confirm or replace `MARIADB_ROOT_PASSWORD`, `MARIADB_PASSWORD`, `SECRET_KEY`, host, port, and enabled cryptos in an interactive terminal
- build and start the stack

Direct entrypoint:

```bash
bash deploy/shkeeperctl.sh install
```

If you still want to use the manual quickstart flow, create `.env` yourself and then run:

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml up --build -d
```

### 4. Build And Start

The install script above already builds and starts the stack. The manual command is shown in step 3 for users who do not want the interactive installer.

The Docker build compiles the Vue admin UI first, embeds it into the Go service, then starts the service at `http://127.0.0.1:5000`.

### 5. Verify The Service

```bash
curl http://127.0.0.1:5000/healthz
curl http://127.0.0.1:5000/readyz
```

Expected `readyz` result:

```json
{"status":"ready"}
```

### 6. Set The First Admin Password

Open the browser:

```text
http://127.0.0.1:5000/set-password
```

Set the first admin password for the default `admin` user, then open:

```text
http://127.0.0.1:5000/wallets
```

You can also set the password from the terminal:

```bash
curl -i -X POST http://127.0.0.1:5000/set-password \
  -d 'pw1=change-admin-password' \
  -d 'pw2=change-admin-password'
```

### 7. Stop Or Reset

Stop the containers but keep the MariaDB volume:

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml down
```

Remove containers and local database volume:

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml down -v
```

## Run From Source

Use this path when you want to edit Go code directly.

Start MariaDB:

```bash
docker compose --env-file .env -f docker-compose.quickstart.yml up -d mariadb
```

Run the app:

```bash
export MARIADB_DATABASE_URL='mariadb://shkeeper:change-db-password@127.0.0.1:3306/shkeeper'
export SECRET_KEY='change-cookie-secret'
export SHKEEPER_CRYPTOS='USDT'
go run ./cmd/shkeeper
```

PowerShell:

```powershell
$env:MARIADB_DATABASE_URL = 'mariadb://shkeeper:change-db-password@127.0.0.1:3306/shkeeper'
$env:SECRET_KEY = 'change-cookie-secret'
$env:SHKEEPER_CRYPTOS = 'USDT'
go run ./cmd/shkeeper
```

## Admin UI Development

The production image builds `web/admin` and copies the generated assets into `internal/app/admin_dist`.

```bash
cd web/admin
npm install
npm run build
```

During UI development:

```bash
cd web/admin
npm install
npm run dev
```

The Vite dev server is for frontend iteration. The Go service remains the production runtime and serves the embedded build.

## Configuration

Important environment variables:

- `MARIADB_DATABASE_URL` or `DATABASE_URL`: MariaDB/MySQL DSN, for example `mariadb://user:pass@host:3306/shkeeper`.
- `SECRET_KEY`: cookie signing secret.
- `SHKEEPER_BACKEND_KEY`: shared secret used by chain workers when notifying the main service; the runtime rejects notifications when it is unset.
- `METRICS_USERNAME` and `METRICS_PASSWORD`: required credentials for the main `/metrics` endpoint.
- `SHKEEPER_COOKIE_SECURE`: sets the `Secure` flag on admin and 2FA cookies; enable it behind production HTTPS.
- `SHKEEPER_LISTEN`: listen address, default `:5000`.
- `SHKEEPER_CRYPTOS`: comma-separated enabled crypto list, for example `BTC,TRX,USDT`.
- `SCHEDULER_ENABLED`: enables background payout and notification tasks. The quickstart compose disables it by default.
- `DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS`, `DB_CONN_MAX_IDLE_SECONDS`, `DB_CONN_MAX_LIFETIME_SECONDS`: MariaDB pool tuning.

SQLite, SQLite files, PostgreSQL, and `SQLALCHEMY_DATABASE_URI` are rejected by the Go runtime. Use MariaDB so the service keeps the legacy SHKeeper table and index contract.

## API Overview

Merchant APIs use `X-Shkeeper-Api-Key`.

```http
GET /api/v1/crypto
POST /api/v1/{crypto}/payment_request
GET /api/v1/orders
GET /api/v1/orders/{external_id}
GET /api/v1/invoices
GET /api/v1/invoices/{external_id}
GET /api/v1/transactions
GET /api/v1/{crypto}/status
POST /api/v1/{crypto}/payout
POST /api/v1/walletnotify/{crypto}/{txid}
```

Admin APIs and pages are protected by the admin session or HTTP Basic Auth:

```http
GET /wallets
GET /settings
PATCH /api/v1/admin/account
GET /api/v1/admin/wallets
GET /api/v1/admin/orders
GET /api/v1/admin/payouts
```

## Chain Workers

`/app/chain-worker` provides Go worker implementations for sidecar-style crypto services. Workers persist generated accounts and payout tasks in MariaDB and expose legacy-compatible endpoints such as:

```http
GET /readyz
POST /{crypto}/status
POST /{crypto}/balance
POST /{crypto}/generate-address
POST /{crypto}/payout
GET /{crypto}/task/{id}
```

Use `docker-compose.example.yml` as the full multi-worker template. Configure fullnode URLs, worker BasicAuth, account passwords, token contracts, and RPC credentials before enabling real payment or payout flows.

## Migration From Legacy SHKeeper

Fresh deployments only need an empty MariaDB database. Existing SHKeeper installations can use:

- `/app/import-legacy-main-mariadb` for MariaDB-to-MariaDB main-service data import.
- `/app/import-legacy-json` for already exported offline legacy bundles.
- `/app/import-legacy-accounts` for sidecar account import and Go `v1:` AES-GCM secret rewriting.
- `/app/cutover-preflight`, `/app/deploy-check`, `/app/cutover-audit`, `/app/post-cutover-verify`, `/app/release-audit`, and `/app/goal-audit` for staged verification.

Keep production secrets out of the repository and pass them through environment files, Docker secrets, or mounted secret files.

## Tests And Verification

```bash
go test ./...
```

With a MariaDB integration database:

```bash
export GO_SHKEEPER_TEST_DATABASE_URL='mariadb://user:pass@127.0.0.1:3306/go_shkeeper_test'
go test ./...
```

Runtime image audit:

```bash
docker build -t go-shkeeper .
docker run --rm go-shkeeper /app/runtime-audit
```

## Project Layout

```text
cmd/                      Go command entrypoints
internal/app/             Main service, admin UI routes, API handlers, schema
internal/chainworker/     Chain worker adapters and storage
internal/*audit/          Cutover, release, runtime, and goal audit packages
web/admin/                Vue/Vite admin UI source
internal/app/admin_dist/  Embedded admin UI build output
deploy/                   Production deployment and verification assets
docker-compose.quickstart.yml  Minimal first-run stack
docker-compose.example.yml     Full multi-worker example stack
```

## Security Notes

- Do not commit `.env`, private keys, wallet seeds, API keys, or admin passwords.
- Replace every sample secret before exposing the service outside localhost.
- Use HTTPS and a reverse proxy in production.
- Run small-value real-chain tests before enabling payouts.
- Keep MariaDB backups before imports, migrations, and cutovers.

## License

See [LICENSE](LICENSE).
