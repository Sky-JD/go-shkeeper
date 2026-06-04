# Go SHKeeper

`go-shkeeper` is a Go rewrite of the SHKeeper application layer. It keeps the existing table names and API surface where practical, while adding a complete order query API and an authenticated admin account update flow.

## Highlights

- No legacy runtime packages in the service image. `/app/runtime-audit` verifies the final container has the expected Go binaries and no `python`, `pip`, or `sqlite3` command on `PATH`.
- MariaDB-only storage. The service uses `database/sql` with the MariaDB/MySQL wire protocol and explicitly rejects `sqlite://`/SQLite file URLs. Legacy main-service synchronization is Go-native MariaDB to MariaDB through `/app/import-legacy-main-mariadb`; SQLite is not a Go runtime or deployment database.
- Compatible with the existing SHKeeper invoice, transaction, payout, wallet, exchange rate, and user tables.
- Complete order lookup through `/api/v1/orders` and `/api/v1/orders/{external_id}`. The response includes unpaid, partial, paid, overpaid, cancelled, refunded, outgoing records, payout-only external IDs, confirmed transactions, unconfirmed transactions, addresses, and payouts.
- Admin username and password can be changed from `/settings`, `PATCH /api/v1/admin/account`, or the Go-native `/app/admin-account` MariaDB CLI for cutover/reset workflows.
- Admin 2FA is implemented in Go with TOTP, pending-login cookies, and one-time backup codes stored as bcrypt hashes in MariaDB.
- Compatibility endpoints for existing integrations: `/api/v1/{crypto}/status`, payment-gateway settings, payout destinations, autopayout settings, exchange-rate updates, fee estimation, payout amount checks, `/api/v1/{crypto}/fee-deposit-address`, `/api/v1/{crypto}/task/{id}`, `/api/v1/decryption-key`, and `/metrics`.
- Legacy admin URLs such as `/wallet/{crypto}`, `/payout/{crypto}`, `/unlock`, `/settings/locale`, `/configure/tron`, `/parts/tron-multiserver`, and `/parts/tron-staking-stake` are served by the Go application.
- Uses Go `database/sql`, bounded connection pools, short HTTP timeouts, in-memory TTL caches, and goroutine workers for fast low-memory API responses.
- Crypto backends are modular. The repository builds `/app/shkeeper`, `/app/chain-worker`, `/app/admin-account`, `/app/worker-serverkey`, `/app/deploy-check`, `/app/final-plan`, `/app/cutover-preflight`, `/app/import-legacy-main-mariadb`, `/app/import-legacy-json`, `/app/import-legacy-accounts`, `/app/cutover-audit`, `/app/post-cutover-verify`, `/app/release-audit`, `/app/runtime-audit`, and `/app/goal-audit`; BTC, LTC, DOGE, FIRO/FIRO-SPARK, BTC Lightning, TRON, BNB, Solana/SPL, XMR, XRP, and EVM-network sidecars can run as Go workers in modular compose files.

## Run

```powershell
cd D:\Java\IdeaProjects\go-shkeeper
go run ./cmd/shkeeper
```

Important environment variables:

- `MARIADB_DATABASE_URL` or `DATABASE_URL`: `mariadb://user:pass@host:3306/dbname`. `MARIADB_DATABASE_URL` wins when both are set. `mysql://...` DSNs are accepted because MariaDB uses the MySQL wire protocol. `sqlite://...`, `sqlite3`, and SQLite file URLs are rejected at startup. The old Python `SQLALCHEMY_DATABASE_URI` variable is not used; replace it with an explicit MariaDB URL.
- `SHKEEPER_LISTEN`: default `:5000`.
- `SECRET_KEY`: signing key for admin session cookies.
- `SHKEEPER_CRYPTOS`: optional comma-separated enabled crypto list, for example `BTC,ETH,TRX,USDT`.
- `SCHEDULER_ENABLED`: `true` by default.
- `DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS`, `DB_CONN_MAX_IDLE_SECONDS`, `DB_CONN_MAX_LIFETIME_SECONDS`: MariaDB connection pool controls for high-concurrency deployments. Defaults are bounded and conservative; tune them against MariaDB `max_connections` and worker count.
- Chain workers use the same `MARIADB_DATABASE_URL`/`DATABASE_URL` format and persist generated accounts/tasks in MariaDB.
- `/healthz` checks that the process is serving HTTP. `/readyz` also pings MariaDB and should be used for rollout readiness checks.

### Database Choice

Use MariaDB for the current Go conversion. The legacy SHKeeper deployment already uses MySQL/MariaDB-compatible table names, indexes, timestamp defaults, `ON DUPLICATE KEY` upserts, decimal columns, and sidecar account/task storage. The Go code keeps that contract with `database/sql` plus the MySQL wire driver, so data import and final cutover can stay MariaDB-to-MariaDB without a second schema translation.

PostgreSQL/PGDB is not the better fit for this repository right now. It would require a separate schema dialect, rewritten upsert/index migration SQL, query-plan preflight checks, import tooling, compose files, and production cutover scripts. The Go runtime therefore rejects `postgres://`, `postgresql://`, and `pgdb://` URLs and keeps MariaDB as the supported deployment database.

## Docker

```powershell
docker build -t go-shkeeper .
docker run --rm -p 5000:5000 -e MARIADB_DATABASE_URL="mariadb://user:${DB_PASSWORD}@host.docker.internal:3306/shkeeper" go-shkeeper
```

For the hk-16-16 style stack, use [deploy/hk-16-16.modular.example.yml](deploy/hk-16-16.modular.example.yml). It replaces the current main container plus BTC Lightning/TRON/BNB/Solana/XMR/XRP sidecars with Go binaries and leaves secrets as environment placeholders.

## Deployment Check

The image includes `/app/runtime-audit`, a Go-native runtime dependency verifier:

```powershell
docker run --rm go-shkeeper /app/runtime-audit
```

It exits nonzero when the final image is missing the expected Go binaries or exposes forbidden runtime commands such as `python`, `pip`, or `sqlite3`. Override the command/file lists only for local diagnostics with `RUNTIME_AUDIT_FORBIDDEN_COMMANDS` and `RUNTIME_AUDIT_REQUIRED_FILES`.

`/app/goal-audit` combines runtime, MariaDB preflight, final readiness, deploy-check, release-audit, post-cutover, container-inventory, and container-stats reports into a single requirement-by-requirement acceptance report. It exits nonzero until the full Go/MariaDB replacement is proven. Final production audits require strict release gates by default; keep `GOAL_AUDIT_REQUIRE_STRICT_RELEASE_GATES=true` unless running an explicitly non-production smoke check.

```bash
docker run --rm \
  -v /deploy-reports/go-shkeeper-final:/deploy-reports:ro \
  -e GOAL_AUDIT_RUNTIME_AUDIT_FILE=/deploy-reports/runtime-audit.json \
  -e GOAL_AUDIT_PREFLIGHT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json \
  -e GOAL_AUDIT_READINESS_FILE=/deploy-reports/go-shkeeper-final-readiness.json \
  -e GOAL_AUDIT_DEPLOY_REPORT_FILES=/deploy-reports/go-shkeeper-final-deploy-check-report.json \
  -e GOAL_AUDIT_RELEASE_AUDIT_FILE=/deploy-reports/go-shkeeper-release-audit.json \
  -e GOAL_AUDIT_POST_CUTOVER_FILE=/deploy-reports/go-shkeeper-post-cutover.json \
  -e GOAL_AUDIT_CONTAINER_INVENTORY_FILE=/deploy-reports/container-inventory.jsonl \
  -e GOAL_AUDIT_CONTAINER_STATS_FILE=/deploy-reports/container-stats.jsonl \
  -e GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB=512 \
  go-shkeeper /app/goal-audit
```

For cutover or emergency reset, update the MariaDB-backed admin account without Python:

```powershell
docker run --rm --network shkeeper_default `
  -e MARIADB_DATABASE_URL="mariadb://user:${DB_PASSWORD}@mariadb:3306/shkeeper" `
  -e ADMIN_USERNAME="admin" `
  -e ADMIN_PASSWORD_FILE=/run/secrets/admin_password `
  -e ADMIN_ACCOUNT_REPORT_FILE=/deploy-reports/go-shkeeper-admin-account.json `
  -v ${PWD}\admin_password:/run/secrets/admin_password:ro `
  -v /tmp/deploy-reports:/deploy-reports `
  go-shkeeper /app/admin-account
```

For cutover worker BasicAuth, update MariaDB `wallet.serverkey` without Python. If `WORKER_SERVERKEY_CRYPTOS` is omitted, the CLI updates every enabled wallet:

```powershell
docker run --rm --network shkeeper_default `
  -e MARIADB_DATABASE_URL="mariadb://user:${DB_PASSWORD}@mariadb:3306/shkeeper" `
  -e WORKER_SERVERKEY_CRYPTOS="BNB,BNB-USDT,TRX,USDT,USDC" `
  -e WORKER_USERNAME="worker" `
  -e WORKER_PASSWORD_FILE=/run/secrets/worker_password `
  -e WORKER_SERVERKEY_REPORT_FILE=/deploy-reports/go-shkeeper-worker-serverkey.json `
  -v ${PWD}\worker_password:/run/secrets/worker_password:ro `
  -v /tmp/deploy-reports:/deploy-reports `
  go-shkeeper /app/worker-serverkey
```

Before generating a production final plan, run the read-only cutover preflight against the target MariaDB database:

```powershell
docker run --rm --network shkeeper_default `
  -e MARIADB_DATABASE_URL="mariadb://user:${DB_PASSWORD}@mariadb:3306/shkeeper" `
  -e CUTOVER_PREFLIGHT_OUTPUT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json `
  -v /tmp/deploy-reports:/deploy-reports `
  go-shkeeper /app/cutover-preflight
```

It fails with `status=blocked` when required Go runtime tables such as `wallet`, `invoice`, `payout`, or `order_index` are missing, when required order-query indexes are absent, or when no wallet cryptos are enabled. The JSON report records `order_index_rows` plus `EXPLAIN` evidence for hot order-list paths, catching an incomplete MariaDB final sync before any real payment or payout rehearsal starts.

Fresh deployments only need a MariaDB database. When replacing an existing hk-16-16 legacy stack, run the MariaDB final sync steps to preserve old orders, wallets, settings, and sidecar keys; the destination is always MariaDB. Sidecar account import can read old sidecar MariaDB tables directly with `LEGACY_ACCOUNTS_DATABASE_URL`, fetch the old `/decrypt` key in Go with `LEGACY_ACCOUNT_DECRYPT_URL` and `LEGACY_ACCOUNT_BACKEND_KEY`, then rewrite secrets as Go `v1:` AES-GCM values in MariaDB. See [deploy/README.md](deploy/README.md#legacy-main-data-import-and-final-sync) for the exact hk-16-16 commands.

The image also includes `/app/deploy-check`, a Go-native rollout verifier. By default it performs read-only checks and exits nonzero on failure:

```powershell
docker run --rm --network host go-shkeeper /app/deploy-check
```

For repeatable staging or production cutover checks, mount a JSON plan and point `DEPLOY_CHECK_PLAN_FILE` at it:

```powershell
docker run --rm --network shkeeper_default `
  -v ${PWD}\deploy\deploy-check.plan.example.json:/deploy-check.plan.json:ro `
  -v ${PWD}\deploy-reports:/deploy-reports `
  -e DEPLOY_CHECK_PLAN_FILE=/deploy-check.plan.json `
  -e DEPLOY_CHECK_REPORT_FILE=/deploy-reports/go-shkeeper-deploy-check-report.json `
  -e DEPLOY_CHECK_API_KEY="$env:ACTIVE_WALLET_APIKEY" `
  -e DEPLOY_CHECK_ADMIN_PASSWORD="$env:ACTIVE_ADMIN_PASSWORD" `
  go-shkeeper /app/deploy-check
```

Useful environment variables:

- `DEPLOY_CHECK_MAIN_URL`: main service URL, default `http://127.0.0.1:5000`.
- `DEPLOY_CHECK_PLAN_FILE`: optional JSON plan file. It supports `main_url`, `worker_url`, `worker_urls`, `api_key`, `crypto`, `cryptos`, `coverage_cryptos`, order lookup fields, admin credentials, worker credentials, `worker_address_checks`, concurrency limits, status checks, and timeout values. Environment variables override the file so production secrets can stay out of the plan.
- `DEPLOY_CHECK_REPORT_FILE`: optional JSON report path. The verifier writes the report on success and failure, including `ok`, `skipped`, and `failed` checks, timestamps, latency/concurrency details, crypto names, external IDs, and the final error when a gate fails.
- `DEPLOY_CHECK_WORKER_URL`: optional worker URL, for example `http://127.0.0.1:6000`.
- `DEPLOY_CHECK_WORKER_URLS`: optional comma-separated multi-worker readiness list, for example `btc=http://btc-worker:6000,tron=http://tron-worker:6000,xrp=http://xrp-worker:6000`. Each entry checks `/healthz` and `/readyz`.
- `DEPLOY_CHECK_API_KEY`, `DEPLOY_CHECK_ORDER_EXTERNAL_ID`, `DEPLOY_CHECK_EXPECT_STATUS`: verify `/api/v1/orders/{external_id}` returns the requested order and status, including non-success states such as `UNPAID`. The API key must match an existing `wallet.apikey` in MariaDB.
- `DEPLOY_CHECK_ORDER_REQUESTS`, `DEPLOY_CHECK_ORDER_CONCURRENCY`, `DEPLOY_CHECK_ORDER_MAX_LATENCY_MS`: repeat the order lookup concurrently and fail if any request is wrong, errors, or exceeds the latency threshold.
- `DEPLOY_CHECK_ORDER_LIST=true`: verify `/api/v1/orders` directly. Optional filters and gates are `DEPLOY_CHECK_ORDER_LIST_STATUS`, `DEPLOY_CHECK_ORDER_LIST_CRYPTO`, `DEPLOY_CHECK_ORDER_LIST_LIMIT`, `DEPLOY_CHECK_ORDER_LIST_MIN_RESULTS`, `DEPLOY_CHECK_ORDER_LIST_REQUESTS`, `DEPLOY_CHECK_ORDER_LIST_CONCURRENCY`, and `DEPLOY_CHECK_ORDER_LIST_MAX_LATENCY_MS`. Use this in final rehearsal to prove the paginated list path is fast under concurrent load, not only single-order detail lookup.
- `DEPLOY_CHECK_ORDER_STATUS_MATRIX=true`: page through `/api/v1/orders`, discover every invoice/payout status plus crypto pair in the returned orders, then verify each pair through the filtered list API. Optional gates are `DEPLOY_CHECK_ORDER_STATUS_MATRIX_EXPECT_STATUSES`, `DEPLOY_CHECK_ORDER_STATUS_MATRIX_MIN`, `DEPLOY_CHECK_ORDER_STATUS_MATRIX_LIMIT`, and `DEPLOY_CHECK_ORDER_STATUS_MATRIX_PAGES`. This read-only check proves the list path covers non-success statuses and does not mistake nested transaction statuses such as `CONFIRMED` for order statuses.
- `DEPLOY_CHECK_MAIN_STATUS=true`, `DEPLOY_CHECK_CRYPTOS=BTC,LTC,DOGE`, `DEPLOY_CHECK_EXPECT_SERVER_STATUS=Synced`: optionally verify `/api/v1/{crypto}/status` for one or more cryptos. This is read-only and fails when a crypto is `Offline` or reports a balance error.
- `DEPLOY_CHECK_ADMIN_USERNAME`, `DEPLOY_CHECK_ADMIN_PASSWORD`, `DEPLOY_CHECK_CRYPTO`: verify admin BasicAuth against `GET /api/v1/{crypto}/server`.
- `DEPLOY_CHECK_WORKER_USERNAME`, `DEPLOY_CHECK_WORKER_PASSWORD`, `DEPLOY_CHECK_WORKER_STATUS=true`: optionally call the worker `/{crypto}/status` endpoint with BasicAuth.
- `DEPLOY_CHECK_WORKER_TASK_ID`: optionally verify worker task lookup through `/{crypto}/task/{id}`.
- `DEPLOY_CHECK_CONCURRENCY`, `DEPLOY_CHECK_REQUESTS`: repeat `/readyz` concurrently to catch obvious pool/readiness regressions before traffic cutover.
- `DEPLOY_CHECK_COVERAGE_CRYPTOS`: optional comma-separated chain/token coverage list. When set, payment/payout coverage gates use this list instead of the shorter status-check `cryptos` list.
- `DEPLOY_CHECK_REQUIRE_PAYMENT_COVERAGE=true`, `DEPLOY_CHECK_REQUIRE_PAYOUT_COVERAGE=true`: fail before mutating probes unless every crypto listed in `DEPLOY_CHECK_COVERAGE_CRYPTOS`, plan `coverage_cryptos`, or the fallback `cryptos` list has a matching `payment_checks` or `payout_checks` entry. Use these gates for final real-chain rehearsal plans so BTC-only checks cannot accidentally stand in for full-chain/token coverage.

Mutating checks are disabled unless `DEPLOY_CHECK_MUTATING=true` is explicitly set. With that flag, `DEPLOY_CHECK_ADMIN_UPDATE_USERNAME` and `DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD` run a forward-and-revert account update round trip, verifying the updated credentials after the forward change and the original credentials after revert. JSON plans can include `worker_address_checks`; each item calls a named worker's `/{crypto}/generate-address` endpoint with BasicAuth and records the generated address, proving the modular worker can execute account creation and persist the MariaDB-backed account row. JSON plans can include `payment_checks`; each item creates a real `POST /api/v1/{crypto}/payment_request`, verifies the generated wallet/address, then verifies `/api/v1/orders/{external_id}` includes the unpaid invoice. JSON plans can also include `payout_checks`; each item dispatches a real `POST /api/v1/{crypto}/payout`, then verifies `/payout/status` and `/api/v1/orders/{external_id}` when an API key is available. Use payout checks only for intentional small-value real-chain rehearsals.

`/app/final-plan` can generate a deploy-check plan from MariaDB wallet rows. It leaves `api_key` and worker BasicAuth as placeholders by default; set `FINAL_PLAN_USE_WALLET_API_KEY=true` only in a controlled report directory to copy the first enabled `wallet.apikey` into the generated JSON. Set `FINAL_PLAN_USE_WALLET_SERVERKEY=true` to fill worker address-proof BasicAuth from `wallet.serverkey` values saved as `username:password`; `/app/worker-serverkey` can write those values and emits a report without password material. Explicit `FINAL_PLAN_API_KEY`, `FINAL_PLAN_WORKER_USERNAME_*`, and `FINAL_PLAN_WORKER_PASSWORD_*` values always take precedence. Secret values can also be supplied with `*_FILE`, for example `FINAL_PLAN_API_KEY_FILE`, `FINAL_PLAN_ADMIN_PASSWORD_FILE`, `FINAL_PLAN_ADMIN_UPDATE_PASSWORD_FILE`, `FINAL_PLAN_WORKER_PASSWORD_FILE`, and per-worker `FINAL_PLAN_WORKER_PASSWORD_TRON_FILE`. `FINAL_PLAN_ADMIN_USERNAME` / `FINAL_PLAN_ADMIN_PASSWORD` override admin credentials, while `ADMIN_USERNAME` / `ADMIN_PASSWORD` are accepted as fallbacks so the same values used by `/app/admin-account` can feed the final plan.

The image also includes `/app/cutover-audit`. Run it after one or more `deploy-check` reports have been produced to fail the cutover when required cryptos or workers are missing evidence:

```powershell
docker run --rm `
  -v ${PWD}\deploy\deploy-check.plan.json:/deploy-check.plan.json:ro `
  -v ${PWD}\deploy-reports:/deploy-reports:ro `
  -e CUTOVER_AUDIT_PLAN_FILE=/deploy-check.plan.json `
  -e CUTOVER_AUDIT_REPORT_FILES=/deploy-reports/go-shkeeper-deploy-check-report.json `
  -e CUTOVER_AUDIT_OUTPUT_FILE=/deploy-reports/go-shkeeper-cutover-audit.json `
  -e CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true `
  -e CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS=true `
  -e CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID=true `
  go-shkeeper /app/cutover-audit
```

By default `cutover-audit` requires `order_status_matrix`, `main_status`, `payment_order`, `payout_order`, and worker readiness for the plan coverage cryptos/workers. It reads `coverage_cryptos` first, then falls back to `cryptos` or the deploy-check report. For final modular-worker proof, set `CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS=true` so every expected worker must have a successful `worker_address` check from `deploy-check`. For final real-chain payout rehearsal, also set `CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID=true` so every required crypto must have a recorded payout txid in the deploy-check report. Disable a requirement only for a narrower rehearsal with `CUTOVER_AUDIT_REQUIRE_PAYMENT_ORDER=false`, `CUTOVER_AUDIT_REQUIRE_PAYOUT_ORDER=false`, or the matching `ORDER_STATUS_MATRIX`/`MAIN_STATUS`/`WORKER_READY`/`WORKER_ADDRESS` variables.

After the production containers are replaced, `/app/post-cutover-verify` can combine live readiness checks, deploy reports, cutover audit output, and a host container inventory into one acceptance artifact:

```powershell
docker ps --format '{{json .}}' > deploy-reports/container-inventory.jsonl
docker run --rm --network shkeeper_default `
  -v ${PWD}\deploy-reports:/deploy-reports:ro `
  -e POST_CUTOVER_MAIN_URL=http://shkeeper:5000 `
  -e POST_CUTOVER_WORKER_URLS=btc=http://btc-worker:6000,ltc=http://ltc-worker:6000,doge=http://doge-worker:6000,firo=http://firo-worker:6000,lightning=http://btc-lightning-worker:6000,eth=http://eth-worker:6000,tron=http://tron-worker:6000,bnb=http://bnb-worker:6000,polygon=http://polygon-worker:6000,avalanche=http://avalanche-worker:6000,arbitrum=http://arbitrum-worker:6000,optimism=http://optimism-worker:6000,xmr=http://xmr-worker:6000,xrp=http://xrp-worker:6000,solana=http://solana-worker:6000 `
  -e POST_CUTOVER_DEPLOY_REPORT_FILES=/deploy-reports/go-shkeeper-deploy-check-report.json `
  -e POST_CUTOVER_AUDIT_FILE=/deploy-reports/go-shkeeper-cutover-audit.json `
  -e POST_CUTOVER_CONTAINER_INVENTORY_FILE=/deploy-reports/container-inventory.jsonl `
  -e POST_CUTOVER_EXPECTED_CONTAINERS=go-shkeeper=go-shkeeper,go-btc-worker=go-shkeeper,go-ltc-worker=go-shkeeper,go-doge-worker=go-shkeeper,go-firo-worker=go-shkeeper,go-btc-lightning-worker=go-shkeeper,go-eth-worker=go-shkeeper,go-tron-worker=go-shkeeper,go-bnb-worker=go-shkeeper,go-polygon-worker=go-shkeeper,go-avalanche-worker=go-shkeeper,go-arbitrum-worker=go-shkeeper,go-optimism-worker=go-shkeeper,go-solana-worker=go-shkeeper,go-xmr-worker=go-shkeeper,go-xrp-worker=go-shkeeper `
  -e POST_CUTOVER_FORBIDDEN_CONTAINERS=shkeeper,bnb-shkeeper,bnb_tasks,tron-shkeeper,tron_tasks `
  -e POST_CUTOVER_EXPECTED_IMAGES=go-shkeeper `
  -e POST_CUTOVER_FORBIDDEN_IMAGES=ghcr.io/sky-jd/shkeeper.io-zh,vsyshost/bnb-shkeeper,vsyshost/tron-shkeeper,python: `
  -e POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX=true `
  -e POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID=true `
  -e POST_CUTOVER_OUTPUT_FILE=/deploy-reports/go-shkeeper-post-cutover.json `
  go-shkeeper /app/post-cutover-verify
```

`POST_CUTOVER_EXPECTED_CONTAINERS` accepts comma-separated `name=image-substring` entries and fails when a required container is absent or still running a non-Go image. `POST_CUTOVER_FORBIDDEN_CONTAINERS` catches legacy sidecar names even if a local image tag changed. Post-cutover verification requires the cutover audit to prove `order_status_matrix` by default; keep `POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID=true` for final real-chain payout acceptance.

## Chain Worker

The `chain-worker` binary is the Go module target for sidecar services such as `bitcoin-shkeeper`, `bitcoin-lightning-shkeeper`, `tron-shkeeper`, `tron_tasks`, `bnb-shkeeper`, `bnb_tasks`, `solana-shkeeper`, `monero-shkeeper`, and `xrp-shkeeper`.

Implemented now:

- Basic-auth compatible module endpoints.
- MariaDB-backed account/task tables.
- AES-GCM encrypted private-key storage via `ACCOUNT_PASSWORD`.
- BTC HTTP sidecar compatibility backed by Bitcoin Core JSON-RPC: status, balance, address generation, transaction lookup, fee estimation, fee deposit account, payout, multipayout, and task records.
- Modular Go workers for LTC, DOGE, FIRO, and FIRO-SPARK. They proxy wallet JSON-RPC from `/app/chain-worker`; FIRO-SPARK uses the Spark-specific RPC calls `getsparkbalance`, `getnewsparkaddress`, `spendspark`, `getsparkcoinaddr`, and `getallsparkaddresses`.
- BTC Lightning HTTP sidecar compatibility backed by LND REST: status, channel balance, invoice generation, invoice lookup by `r_hash`, payout through payment requests or LNURL via LNbits, fee deposit LNURL, multipayout, and task records.
- BNB and TRON address generation.
- BNB/TRON fullnode status checks.
- BNB native and BEP20 balance aggregation for generated accounts.
- TRX and TRC20 balance aggregation for generated accounts.
- Local private-key signing and broadcast for BNB, BEP20, TRX, and TRC20 payouts.
- TRON fee-deposit account, multiserver status/change, account resource inspection, and `freezebalancev2` staking endpoints for the old TRON admin workflow.
- `/{crypto}/transaction/{txid}` parsing for BNB/TRON native transfers and standard token `Transfer` events, so payout confirmations and wallet notifications can use the Go worker.
- Payout task status/result persistence in MariaDB for the main API polling flow.
- XMR status, wallet balance, address generation, address listing, transaction lookup, fee deposit account, payout, and task records through Monero daemon RPC plus `monero-wallet-rpc`.
- XRP status, wallet balance, X-address generation with destination tags, address listing, transaction lookup, payout submit, multipayout, and task records through rippled JSON-RPC.
- Solana status, SOL/SPL balance aggregation, ed25519 address generation, ATA-aware SOL/SPL payouts, transaction lookup, multipayout, and task records through Solana JSON-RPC. `SOLANA-USDT` and `SOLANA-USDC` use SPL Token; `SOLANA-PYUSD` uses Token-2022 by default.
- Generic EVM worker support for `CHAIN_MODULE=ETH`, `MATIC`, `AVAX`, `ARBETH`, and `OPETH`, sharing the same address, balance, payout, transaction parsing, and MariaDB task flow as BNB.

Recommended before production payout replacement:

- Configure BTC worker RPC credentials with `BTC_RPC_USERNAME` and `BTC_RPC_PASSWORD`; these are separate from worker BasicAuth `BTC_USERNAME` and `BTC_PASSWORD`.
- Configure LTC/DOGE/FIRO worker RPC endpoints with `LTC_RPC_URL`, `DOGE_RPC_URL`, `FIRO_RPC_URL` plus `LTC_RPC_USERNAME`, `DOGE_RPC_USERNAME`, `FIRO_RPC_USERNAME` and the matching password variables when those wallets are enabled. The main service talks to `ltc-worker`, `doge-worker`, and `firo-worker` over the same module API as the other sidecars.
- Configure BTC Lightning with `LND_REST_URL` plus either `LND_MACAROON_HEX` or `LND_MACAROON_FILE`; use `LND_TLS_SKIP_VERIFY=true` only for trusted internal/self-signed LND endpoints.
- Run small-value mainnet payout tests for every enabled token contract and fullnode provider.
- Configure token contract and decimal overrides when needed: `ETH_USDT_CONTRACT`, `ETH_USDT_DECIMALS`, `POLYGON_USDC_CONTRACT`, `POLYGON_USDC_DECIMALS`, `BNB_USDT_CONTRACT`, `TRON_USDT_CONTRACT`, and the matching `*_DECIMALS` variables for EVM tokens.
- Tune `TRON_TOKEN_FEE_LIMIT_SUN` and account gas/resource funding policy for TRC20 payouts. Use `TRON_FULLNODE_URLS` for comma-separated multiserver choices. Optional TRON staking/fee-deposit overrides are `TRON_FEE_DEPOSIT_ADDRESS`, `TRON_FEE_DEPOSIT_PRIVATE_KEY_HEX`, `TRON_STAKING_ADDRESS`, and `TRON_STAKING_PRIVATE_KEY_HEX`; without them the worker uses/generated MariaDB-backed TRX accounts encrypted by `ACCOUNT_PASSWORD`.
- Configure XMR with `MONERO_DAEMON_URL`, `MONERO_WALLET_RPC_URL`, and the matching RPC credentials when the endpoints require BasicAuth.
- Configure XRP with `XRP_ACCOUNT_ADDRESS` for production receive addresses. `generate-address` returns X-addresses with random destination tags. Payout submit requires `XRP_ACCOUNT_SECRET` on a private/trusted rippled endpoint.
- Configure Solana with `SOLANA_FULLNODE_URL` or `SOLANA_RPC_URL`, `SOLANA_ACCOUNT_PASSWORD`, and optional RPC BasicAuth credentials. SPL defaults are `SOLANA_USDT_MINT`, `SOLANA_USDC_MINT`, and `SOLANA_PYUSD_MINT`; override `SOLANA_*_TOKEN_PROGRAM` for Token-2022 or custom mints.

For EVM token modules without a built-in default contract, both `*_CONTRACT` and `*_DECIMALS` must be set. For Solana token modules without a built-in default mint, both `*_MINT` and `*_DECIMALS` must be set. This prevents sending a token payout with the wrong contract, mint, or unit precision.

## New Order Query API

```http
GET /api/v1/orders/{external_id}
GET /api/v1/orders?external_id=abc&status=UNPAID&crypto=BTC&limit=50&cursor=...
```

Both endpoints require `X-Shkeeper-Api-Key`. Unlike `/api/v1/tx-info/{txid}/{external_id}`, the order APIs do not require a successful transaction to return data.
The order list is built from the union of invoice and payout external IDs, so outgoing rows and payout-only records are still returned.
For high-volume stores, use the returned `next_cursor` for the next page. `offset` is still accepted for compatibility, but cursor pagination avoids deep-offset scans.

## Compatibility API Notes

- `GET /api/v1/{crypto}/status` returns the legacy status shape with wallet balance source/error details.
- `GET /api/v1/{crypto}/generate-address` generates a new address through the configured Go worker or direct JSON-RPC adapter.
- `POST /api/v1/{crypto}/transaction` keeps the legacy manual transaction endpoint and persists the transaction into MariaDB order details.
- `GET|POST /api/v1/{crypto}/payment-gateway` reads or updates the MariaDB `wallet.enabled` gateway flag.
- `POST /api/v1/{crypto}/payment-gateway/token` updates wallet API keys in MariaDB.
- `POST /api/v1/{crypto}/payout_destinations` supports `add`, `delete`, and `list` actions on the MariaDB `payout_destination` table.
- `POST /api/v1/{crypto}/autopayout` updates MariaDB wallet payout policy, reserve policy, limits, confirmations, recalc interval, destination, and fee.
- `POST /api/v1/{crypto}/exchange-rate` updates an existing MariaDB `exchange_rate` row.
- `GET /api/v1/{crypto}/estimate-tx-fee/{amount}` proxies worker fee estimation or uses JSON-RPC `estimatesmartfee` for direct RPC modules.
- `GET /api/v1/{crypto}/payouts?amount=...` checks MariaDB payout rows for the requested crypto and amount and returns matching payout details.
- `POST /api/v1/{crypto}/payout` and `/multipayout` create MariaDB payout rows before worker dispatch. Worker failures are preserved as `FAIL`, worker task IDs/txids are attached after success, and MariaDB unique indexes prevent concurrent duplicate payout `external_id` rows per crypto.
- `GET /api/v1/{crypto}/fee-deposit-address` proxies to workers that support fee-deposit accounts.
- `GET /api/v1/{crypto}/task/{id}` proxies modular worker task status for async payouts.
- `POST /api/v1/walletnotify/{crypto}/{txid}` records incoming notifications and also persists `send` category transfers as `OUTGOING` records with `external_id=outgoing:{txid}`, so transaction and order queries do not lose outgoing wallet activity.
- `GET /api/v1/{crypto}/decrypt` returns wallet-encryption status for worker compatibility. Go workers use `ACCOUNT_PASSWORD` and this endpoint does not return a cleartext key.
- `GET /api/v1/{crypto}/server` returns the effective worker/RPC host and BasicAuth pair. `POST /server/key` and `/server/host` persist MariaDB overrides that take precedence over environment defaults for later worker/RPC calls.
- `GET /api/v1/{crypto}/backup` downloads worker account backup JSON with encrypted private-key payloads where the Go worker owns local accounts.
- `/2fa/setup`, `/2fa/verify`, `/2fa/disable`, and `/2fa/regenerate-backup` preserve the old admin 2FA workflow entirely inside the Go service.
- `POST /api/v1/decryption-key` accepts form or JSON `key`. Go workers do not persist the cleartext key; when a migrated `WalletEncryptionPasswordHash` exists, the key is verified with bcrypt and only runtime status is recorded in MariaDB.
- `POST /api/v1/test-callback-receiver` logs callback payloads and returns HTTP 202.
- `GET /metrics` exposes lightweight Prometheus text metrics protected by `METRICS_USERNAME` and `METRICS_PASSWORD`.
