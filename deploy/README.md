# Modular Deployment

`hk-16-16.modular.example.yml` is the MariaDB-only target layout for replacing the current main service and chain sidecar containers with Go binaries from this project. SQLite URLs and SQLite files are not supported by the Go service or workers.

`evm-worker.example.yml` is a copyable overlay template for additional EVM-network workers such as Ethereum, Polygon, Avalanche, Arbitrum, and Optimism. Use it with an existing compose stack that already defines `mariadb`, for example `docker compose -f docker-compose.example.yml -f deploy/evm-worker.example.yml config`.

The file intentionally uses environment-variable placeholders. Do not copy live secrets into the repository.

Tune `DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS`, `DB_CONN_MAX_IDLE_SECONDS`, and `DB_CONN_MAX_LIFETIME_SECONDS` per deployment. Keep the sum of main-service and worker `DB_MAX_OPEN_CONNS` below the MariaDB `max_connections` budget.

The compose examples use `/readyz` healthchecks for the main service and Go workers. `/readyz` verifies the process can reach MariaDB, so rollout automation can distinguish a live process from a database-ready service.

`shkeeperctl.sh` is the daily Docker management wrapper. It initializes `.env`, builds and starts only the enabled crypto workers, upgrades the checked-out source, stops or removes the stack, and keeps `SHKEEPER_CRYPTOS` plus matching `*_WALLET` switches in sync. In an interactive terminal, first install asks for bind IP, host port, and a numbered multi-select crypto/network list. Non-interactive installs can use `SHKEEPER_HOST`, `SHKEEPER_PORT`, and `SHKEEPER_INIT_CRYPTOS` instead.

```bash
cd /root/go-shkeeper
bash deploy/shkeeperctl.sh install
shkeeperctl
bash deploy/shkeeperctl.sh configure
bash deploy/shkeeperctl.sh enable-crypto TRX USDT BNB-USDT
bash deploy/shkeeperctl.sh show-cryptos
bash deploy/shkeeperctl.sh upgrade
```

`install` installs `/usr/local/bin/shkeeperctl` when permissions allow it. Running `shkeeperctl` without arguments opens a management panel for install, update, uninstall, status, logs, crypto selection, admin password, wallet API key, backend key, and worker serverkey actions. Direct commands remain available, for example `shkeeperctl set-api-key /secure/api_key`, `shkeeperctl admin-password admin /secure/admin_password`, and `shkeeperctl worker-serverkey BNB,BNB-USDT worker /secure/worker_password`.

For the hk modular compose file, pass `SHKEEPER_COMPOSE_FILE=deploy/hk-16-16.modular.example.yml`. `SHKEEPER_DRY_RUN=1` prints Docker/Git actions while still validating and updating the local `.env` crypto configuration. `uninstall` is guarded with `CONFIRM_UNINSTALL=GO_SHKEEPER`, and data volume removal additionally requires `PURGE_DATA=1 CONFIRM_PURGE=DELETE_GO_SHKEEPER_DATA`.

After starting a candidate stack, run the Go-native verifier from the image. It checks main `/healthz`, main `/readyz`, optional complete order lookup, optional worker `/healthz` and `/readyz`, and optional authenticated worker/admin probes:

```bash
docker run --rm go-shkeeper /app/runtime-audit

docker run --rm --network host \
  -v /tmp:/tmp \
  -e DEPLOY_CHECK_MAIN_URL=http://127.0.0.1:5000 \
  -e DEPLOY_CHECK_WORKER_URL=http://127.0.0.1:6000 \
  -e DEPLOY_CHECK_REPORT_FILE=/tmp/go-shkeeper-deploy-check-report.json \
  -e DEPLOY_CHECK_API_KEY="$SUGGESTED_WALLET_APIKEY" \
  -e DEPLOY_CHECK_ORDER_EXTERNAL_ID="$KNOWN_UNPAID_OR_PAYOUT_EXTERNAL_ID" \
  -e DEPLOY_CHECK_EXPECT_STATUS=UNPAID \
  -e DEPLOY_CHECK_ORDER_REQUESTS=50 \
  -e DEPLOY_CHECK_ORDER_CONCURRENCY=10 \
  -e DEPLOY_CHECK_ORDER_MAX_LATENCY_MS=500 \
  -e DEPLOY_CHECK_ORDER_LIST=true \
  -e DEPLOY_CHECK_ORDER_LIST_STATUS=UNPAID \
  -e DEPLOY_CHECK_ORDER_LIST_CRYPTO=BTC \
  -e DEPLOY_CHECK_ORDER_LIST_REQUESTS=50 \
  -e DEPLOY_CHECK_ORDER_LIST_CONCURRENCY=10 \
  -e DEPLOY_CHECK_ORDER_LIST_MAX_LATENCY_MS=500 \
  go-shkeeper /app/deploy-check
```

For a temporary read-only main-service check against an already migrated MariaDB database, run the Go main service with `SCHEDULER_ENABLED=false`, `SHKEEPER_MIGRATE_ON_START=false`, and `SHKEEPER_ENSURE_CURRENCIES_ON_START=false`. This lets `/app/deploy-check` exercise `/api/v1/orders`, order status matrix, and parallel latency paths without startup migrations, currency registration writes, callbacks, or payout polling.

On hk-16-16, run the repeatable MariaDB-only staging rehearsal before any production replacement. It builds the Go image when needed, creates an isolated MariaDB database, imports the old main data through `/app/import-legacy-main-mariadb`, imports legacy BNB accounts through `/app/import-legacy-accounts` when the old backend key is available, starts temporary Go main/BNB/TRON containers, runs payment/admin/order-list checks, and finishes with `/app/cutover-audit` plus a non-production `/app/release-audit` pass:

```bash
cd /path/to/shkeeper.io-zh
bash go-shkeeper/deploy/hk-16-16-staging-rehearsal.sh
```

Set `GO_SHKEEPER_IMAGE=go-shkeeper:tag BUILD_IMAGE=0` to reuse an already built image. Set `KEEP_REHEARSAL_REPORTS=1` to keep `/tmp/hk-rehearsal-*` reports or `KEEP_REHEARSAL_DB=1` to keep the temporary MariaDB database for manual inspection. The rehearsal does not replace the production `shkeeper`, `bnb-shkeeper`, or `tron-shkeeper` containers.

The staging release audit intentionally disables final payout coverage, payout txid, and post-cutover container gates because it does not dispatch real-chain withdrawals or replace production containers. The production script keeps those gates enabled.

The staging rehearsal also stress-checks the ready endpoint and order list path with configurable parallel load. Defaults are `REHEARSAL_READY_REQUESTS=200`, `REHEARSAL_READY_CONCURRENCY=50`, `REHEARSAL_ORDER_LIST_REQUESTS=200`, `REHEARSAL_ORDER_LIST_CONCURRENCY=50`, and `REHEARSAL_ORDER_LIST_MAX_LATENCY_MS=500`. It writes `container-stats.jsonl` with a `docker stats --no-stream` snapshot for the temporary Go main, BNB worker, and TRON worker containers.

Before production replacement, use `hk-16-16-final-readiness.sh` as the final readiness step to generate the final MariaDB-backed plan and readiness report. It builds or reuses the Go image, writes `/app/runtime-audit` to `runtime-audit.json`, records `container-inventory.jsonl`, runs read-only `/app/cutover-preflight`, runs `/app/final-plan`, writes the plan plus readiness JSON into `REPORT_DIR`, and then writes a `goal-audit.json` report. It does not stop production containers.

The final readiness script defaults `FINAL_PLAN_REDACT_SECRETS=true`. When `API_KEY_FILE`, `ADMIN_PASSWORD_FILE`, `ADMIN_UPDATE_PASSWORD_FILE`, `WORKER_PASSWORD_FILE`, or per-worker password files are mounted, `/app/final-plan` records only `secret_override_fields` in the plan/readiness report and keeps placeholder text in the JSON plan. The later `/app/deploy-check` run receives the same files through `DEPLOY_CHECK_*_FILE` variables and fills those placeholders at runtime, so the final plan can be reviewed without cleartext API keys or passwords.

The readiness helper runs one-shot utility containers with `UTILITY_DOCKER_USER=0:0` by default so bind-mounted `0600` secret files remain readable. This does not change the final service image user; the long-running Go main service and workers still use the Dockerfile user unless a compose file explicitly overrides it.

When hk-16-16 SSH is unstable, start the same readiness flow through `hk-16-16-async-final-readiness.sh`. It writes a detached job, log, pid file, and status file under `REPORT_DIR`, then returns immediately so a dropped SSH session does not kill the Docker build or readiness report generation. Use `SOURCE_ARCHIVE=/tmp/go-shkeeper-src.tgz` when running from an uploaded source archive. The async wrapper refuses inline secret values by default; pass `API_KEY_FILE`, `ADMIN_PASSWORD_FILE`, `ADMIN_UPDATE_PASSWORD_FILE`, and worker password file paths instead.
Set `CLEANUP_SECRET_DIRS=/tmp/codex-...-secrets` to have the detached job delete temporary secret directories on exit. The wrapper only removes `/tmp/codex-*secret*` paths, so a typo cannot delete arbitrary directories.

```bash
SOURCE_ARCHIVE=/tmp/go-shkeeper-src.tgz \
REPORT_DIR=/tmp/go-shkeeper-final \
CLEANUP_SECRET_DIRS=/tmp/codex-go-shkeeper-final-secrets \
GO_SHKEEPER_IMAGE=go-shkeeper:final \
API_KEY_FILE=/secure/api_key \
ADMIN_PASSWORD_FILE=/secure/admin_password \
ADMIN_UPDATE_PASSWORD_FILE=/secure/admin_update_password \
WORKER_USERNAME=worker \
WORKER_PASSWORD_FILE=/secure/worker_password \
bash go-shkeeper/deploy/hk-16-16-async-final-readiness.sh

tail -f /tmp/go-shkeeper-final/async-final-readiness.log
cat /tmp/go-shkeeper-final/async-final-readiness.status
```

```bash
REPORT_DIR=/deploy-reports/go-shkeeper-final \
GO_SHKEEPER_IMAGE=go-shkeeper:final \
FINAL_PLAN_USE_WALLET_API_KEY=true \
ADMIN_PASSWORD_FILE=/secure/admin_password \
ADMIN_UPDATE_PASSWORD_FILE=/secure/admin_update_password \
WORKER_USERNAME=worker \
WORKER_PASSWORD_FILE=/secure/worker_password \
DEPLOY_CHECK_PAYOUT_AMOUNT_BNB_USDT=0.01 \
DEPLOY_CHECK_PAYOUT_AMOUNT_TRX=1 \
bash go-shkeeper/deploy/hk-16-16-final-readiness.sh
```

If readiness reports missing worker credentials and you intentionally want to save worker BasicAuth into MariaDB `wallet.serverkey`, run the same script with the guarded write enabled:

```bash
UPDATE_WORKER_SERVERKEY=1 CONFIRM_DB_WRITE=GO_SHKEEPER_HK_16_16 \
FINAL_PLAN_USE_WALLET_SERVERKEY=true \
WORKER_USERNAME=worker \
WORKER_PASSWORD_FILE=/secure/worker_password \
bash go-shkeeper/deploy/hk-16-16-final-readiness.sh
```

The script exits with `final_readiness_blocked` when the generated plan still has placeholder groups such as admin credentials, worker credentials, or payout amounts. It still writes the goal-audit report before exiting, so blocked readiness keeps objective-level evidence for the missing admin round trip, worker address proof, payout amounts, release audit, or post-cutover proof. After readiness is `ready`, the real-chain deploy-check is still opt-in and requires an explicit confirmation because it creates payment requests and dispatches small-value payouts:

```bash
RUN_DEPLOY_CHECK=1 CONFIRM_REAL_CHAIN_REHEARSAL=GO_SHKEEPER_HK_16_16 \
REPORT_DIR=/deploy-reports/go-shkeeper-final \
bash go-shkeeper/deploy/hk-16-16-final-readiness.sh
```

`hk-16-16-production-cutover.sh` is the guarded production entrypoint. It defaults to `DRY_RUN=1`; it refuses to stop legacy containers unless `DRY_RUN=0 CONFIRM_PRODUCTION_CUTOVER=GO_SHKEEPER_HK_16_16` is set. The script requires a completed final deploy-check plan, deploy-check report, and final readiness report, stops legacy write containers, runs the final `/app/import-legacy-main-mariadb` sync, runs `/app/cutover-preflight`, runs strict `/app/cutover-audit` with import/preflight/order-matrix/worker-address/payout-txid gates, starts the Go modular compose services, writes `container-inventory.jsonl` and `container-stats.jsonl`, runs `/app/post-cutover-verify`, writes `/app/release-audit`, and then runs `/app/goal-audit` as the final objective-level acceptance report. If any step after stopping legacy containers fails, including `goal-audit`, it removes Go containers and restarts the legacy containers by default.

```bash
REPORT_DIR=/deploy-reports/go-shkeeper-final \
PLAN_FILE=/deploy-reports/go-shkeeper-final/go-shkeeper-final-cutover.plan.json \
FINAL_DEPLOY_REPORT_FILE=/deploy-reports/go-shkeeper-final/go-shkeeper-final-deploy-check-report.json \
FINAL_READINESS_REPORT_FILE=/deploy-reports/go-shkeeper-final/go-shkeeper-final-readiness.json \
GO_SHKEEPER_IMAGE=go-shkeeper:final \
DRY_RUN=1 \
bash go-shkeeper/deploy/hk-16-16-production-cutover.sh
```

Switch `DRY_RUN=0` only after the final real-chain report has passed with payout txids for every enabled chain/token and the intended maintenance window has started.

`/app/release-audit` is the final offline release gate. It reads the final plan, final deploy-check report, cutover-audit report, and post-cutover report, then fails unless mutating payment/payout coverage, fresh import sync, preflight, order matrix, worker address proof, payout txids, and post-cutover container checks are all proven.

After `release-audit`, run `/app/goal-audit` to produce the objective-level report for the whole rewrite: Go runtime without Python/SQLite, MariaDB preflight, complete order query, admin account change proof, modular worker proof, high-concurrency API evidence, low-memory container evidence, strict release audit, and production container replacement. It is intentionally strict and exits nonzero while final readiness is blocked, container memory stats are absent, memory exceeds `GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB` (default `512` MiB per container), or legacy containers are still running:

```bash
docker ps --format '{{json .}}' > /deploy-reports/go-shkeeper-final/container-inventory.jsonl
docker stats --no-stream --format '{{json .}}' go-shkeeper go-btc-worker go-ltc-worker go-doge-worker go-firo-worker go-btc-lightning-worker go-eth-worker go-tron-worker go-bnb-worker go-polygon-worker go-avalanche-worker go-arbitrum-worker go-optimism-worker go-solana-worker go-xmr-worker go-xrp-worker > /deploy-reports/go-shkeeper-final/container-stats.jsonl

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

If the imported legacy admin password is unknown during a staging rehearsal, set a known MariaDB-backed admin credential with the Go CLI before running mutating admin checks:

```bash
docker run --rm --network shkeeper_default \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper" \
  -e ADMIN_USERNAME="admin" \
  -e ADMIN_PASSWORD_FILE=/run/secrets/admin_password \
  -e ADMIN_ACCOUNT_REPORT_FILE=/deploy-reports/go-shkeeper-admin-account.json \
  -v "$PWD/admin_password:/run/secrets/admin_password:ro" \
  -v /tmp/go-shkeeper-final-reports:/deploy-reports \
  go-shkeeper /app/admin-account
```

The admin account report records the user id, username, timestamp, and `password_updated=true`; it never writes the password. `FINAL_PLAN_ADMIN_USERNAME` / `FINAL_PLAN_ADMIN_PASSWORD` / `FINAL_PLAN_ADMIN_UPDATE_USERNAME` / `FINAL_PLAN_ADMIN_UPDATE_PASSWORD` override generated plan credentials, and `ADMIN_USERNAME` / `ADMIN_PASSWORD` / `ADMIN_UPDATE_USERNAME` / `ADMIN_UPDATE_PASSWORD` are accepted as fallbacks so a cutover script can reuse the same values after running `/app/admin-account`.

For worker BasicAuth, store the same `username:password` pair that the main service will use when calling Go workers into MariaDB `wallet.serverkey`. This lets `/app/final-plan` fill worker address-proof credentials when `FINAL_PLAN_USE_WALLET_SERVERKEY=true`. The report records the affected cryptos and username, but not the password:

```bash
docker run --rm --network shkeeper_default \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper" \
  -e WORKER_SERVERKEY_CRYPTOS="BNB,BNB-USDT,TRX,USDT,USDC" \
  -e WORKER_USERNAME="worker" \
  -e WORKER_PASSWORD_FILE=/run/secrets/worker_password \
  -e WORKER_SERVERKEY_REPORT_FILE=/deploy-reports/go-shkeeper-worker-serverkey.json \
  -v "$PWD/worker_password:/run/secrets/worker_password:ro" \
  -v /tmp/go-shkeeper-final-reports:/deploy-reports \
  go-shkeeper /app/worker-serverkey
```

For real-chain cutover rehearsal, add a crypto list so the verifier calls the main service status endpoint for every enabled module:

```bash
docker run --rm --network host \
  -v /tmp:/tmp \
  -e DEPLOY_CHECK_MAIN_URL=http://127.0.0.1:5000 \
  -e DEPLOY_CHECK_REPORT_FILE=/tmp/go-shkeeper-status-report.json \
  -e DEPLOY_CHECK_API_KEY="$ACTIVE_WALLET_APIKEY" \
  -e DEPLOY_CHECK_CRYPTOS=BTC,LTC,DOGE,FIRO,TRX,BNB,SOL,XMR,XRP \
  -e DEPLOY_CHECK_EXPECT_SERVER_STATUS=Synced \
  go-shkeeper /app/deploy-check
```

For a full modular stack on the compose network, check every Go worker process in one run:

```bash
docker run --rm --network shkeeper_default \
  -v /tmp:/tmp \
  -e DEPLOY_CHECK_MAIN_URL=http://shkeeper:5000 \
  -e DEPLOY_CHECK_REPORT_FILE=/tmp/go-shkeeper-workers-report.json \
  -e DEPLOY_CHECK_WORKER_URLS=btc=http://btc-worker:6000,ltc=http://ltc-worker:6000,doge=http://doge-worker:6000,firo=http://firo-worker:6000,lightning=http://btc-lightning-worker:6000,eth=http://eth-worker:6000,tron=http://tron-worker:6000,bnb=http://bnb-worker:6000,polygon=http://polygon-worker:6000,avalanche=http://avalanche-worker:6000,arbitrum=http://arbitrum-worker:6000,optimism=http://optimism-worker:6000,xmr=http://xmr-worker:6000,xrp=http://xrp-worker:6000,solana=http://solana-worker:6000 \
  go-shkeeper /app/deploy-check
```

Keep `DEPLOY_CHECK_MUTATING` unset for production read-only checks. Set it only in a staging stack when intentionally verifying the admin username/password update round trip, creating payment-request probes, or running small-value real-chain payout probes.

For repeatable checks, copy `deploy-check.plan.example.json` outside the repository, replace the placeholders, and mount it into the verifier:

```bash
docker run --rm --network shkeeper_default \
  -v "$PWD/deploy-check.plan.json:/deploy-check.plan.json:ro" \
  -v "$PWD/deploy-reports:/deploy-reports" \
  -e DEPLOY_CHECK_PLAN_FILE=/deploy-check.plan.json \
  -e DEPLOY_CHECK_REPORT_FILE=/deploy-reports/go-shkeeper-deploy-check-report.json \
  -e DEPLOY_CHECK_API_KEY="$ACTIVE_WALLET_APIKEY" \
  -e DEPLOY_CHECK_ADMIN_PASSWORD="$ACTIVE_ADMIN_PASSWORD" \
  go-shkeeper /app/deploy-check
```

The plan accepts `worker_urls` as an object map, an array of `{ "name": "...", "url": "..." }`, or the same comma-separated string accepted by `DEPLOY_CHECK_WORKER_URLS`. It also accepts `coverage_cryptos` for the full chain/token evidence list; this can be broader than the `cryptos` list used for routine status checks. Environment variables override the file, so secrets and one-off cutover values can remain outside the JSON.

For final real-chain rehearsal, keep generated plans outside the repository and avoid writing cleartext passwords into the JSON whenever possible. `/app/deploy-check` accepts secret file overrides such as `DEPLOY_CHECK_API_KEY_FILE`, `DEPLOY_CHECK_ADMIN_PASSWORD_FILE`, `DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD_FILE`, `DEPLOY_CHECK_WORKER_PASSWORD_FILE`, and per-worker `DEPLOY_CHECK_WORKER_PASSWORD_TRON_FILE`. It can also replace generated payout amount placeholders at runtime with `DEPLOY_CHECK_PAYOUT_AMOUNT` or per-crypto variables such as `DEPLOY_CHECK_PAYOUT_AMOUNT_BNB_USDT`, `DEPLOY_CHECK_PAYOUT_AMOUNT_TRX`, `DEPLOY_CHECK_PAYOUT_AMOUNT_USDT`, and `DEPLOY_CHECK_PAYOUT_AMOUNT_USDC`.

Set `DEPLOY_CHECK_REPORT_FILE` or `report_file` in the plan to keep a machine-readable cutover artifact. Reports are written on both success and failure and include each `ok`, `skipped`, and `failed` check with timestamps, crypto names, external IDs, order list rows, concurrency/latency fields, and the final error. Keep the report with the hk-16-16 rollout notes after real-chain small-value rehearsals.

For read-only order completeness proof, set `DEPLOY_CHECK_ORDER_STATUS_MATRIX=true`. The verifier pages through `/api/v1/orders`, discovers invoice/payout status plus crypto pairs from the response, and then verifies every pair through the filtered list API. Use `DEPLOY_CHECK_ORDER_STATUS_MATRIX_EXPECT_STATUSES=PAID,UNPAID` and `DEPLOY_CHECK_ORDER_STATUS_MATRIX_MIN=2` when validating imported hk-16-16 data that should contain both paid and unpaid orders. The matrix intentionally ignores nested transaction statuses such as `CONFIRMED`, because the order-list filter applies to invoice and payout states.

`/app/cutover-audit` requires an `order_status_matrix` deploy-check entry by default. Set `CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=false` only for a narrow diagnostic run; final cutover evidence should keep it enabled so the audit proves the paginated order list and filtered list paths were exercised, not just a single order detail endpoint.

The plan can include `worker_address_checks` for modular worker proof. Each worker address check requires `mutating=true`, a worker URL from `worker_urls` or an explicit `url`, `crypto`, and worker BasicAuth credentials; it calls `/{crypto}/generate-address`, records the address in the JSON report, and verifies the worker can execute account creation against the shared MariaDB store.

The plan can include `payment_checks` for incoming-payment rehearsal. Each payment check requires `mutating=true`, `api_key`, `crypto`, `fiat`, and `amount`; it creates a real payment request through the main API, verifies the generated address, and confirms the complete order endpoint returns an unpaid invoice for that `external_id`.

The plan can include `payout_checks` for real-chain payout rehearsal. Each payout check requires `mutating=true`, admin credentials, `crypto`, `amount`, and either `destination` or `destination_from_payment`. `destination_from_payment` names a previous `payment_checks[].name` entry and uses the wallet/payment request created by that payment check as the payout destination. The verifier dispatches a real payout through the main API and verifies the payout row plus complete order details by `external_id` when `api_key` is set. Use unique `external_id` values or `external_id_prefix` for repeatable runs.

For final cutover rehearsals, set `require_payment_coverage=true` and `require_payout_coverage=true` in the JSON plan, or set `DEPLOY_CHECK_REQUIRE_PAYMENT_COVERAGE=true` and `DEPLOY_CHECK_REQUIRE_PAYOUT_COVERAGE=true`. These gates compare `DEPLOY_CHECK_COVERAGE_CRYPTOS`, plan `coverage_cryptos`, or the fallback `cryptos` list against `payment_checks` and `payout_checks`, then fail before any mutating probe when a listed chain/token is missing coverage.

`final-cutover.plan.template.json` is the strict hk-16-16 final rehearsal template. It sets `cryptos` and `coverage_cryptos` to the full default modular list, enables `require_payment_coverage`, enables `require_payout_coverage`, enables `order_status_matrix_check`, includes a payment and payout check for every chain/token, and includes one address-generation proof for every worker. `deploy-check` runs the order status matrix after the mutating payment/payout probes, so the matrix must see the generated full-coverage order evidence. Each payout uses `destination_from_payment` to send to the wallet or payment request created by the matching payment check, so the final rehearsal does not require preparing 36 external destination addresses. Copy the template outside the repository, replace every `replace-with-*` value with the live API key, admin credential, worker BasicAuth credentials, and intentional small-value payout amounts, then run it with `/app/deploy-check`. If `mutating=true` and any placeholder remains, `deploy-check` fails immediately with `plan_placeholders` before making HTTP requests or dispatching payouts.

```bash
cp deploy/final-cutover.plan.template.json /tmp/go-shkeeper-final-cutover.plan.json
vi /tmp/go-shkeeper-final-cutover.plan.json

docker run --rm --network shkeeper_default \
  -v /tmp/go-shkeeper-final-cutover.plan.json:/deploy-check.plan.json:ro \
  -v /tmp/go-shkeeper-final-reports:/deploy-reports \
  -e DEPLOY_CHECK_PLAN_FILE=/deploy-check.plan.json \
  go-shkeeper /app/deploy-check
```

The image also includes `/app/final-plan`, a Go-native plan generator. On hk-16-16 it can read the MariaDB `wallet` table and generate a narrower final rehearsal plan for the currently enabled wallet cryptos, or generate the full default modular matrix with `FINAL_PLAN_ALL_CRYPTOS=true`. It still leaves credentials and payout amounts as placeholders unless supplied through environment variables, so `deploy-check` will stop at `plan_placeholders` until the plan is intentionally completed:

By default, `/app/final-plan` does not copy `wallet.apikey` or `wallet.serverkey` into the generated JSON plan. Set `FINAL_PLAN_USE_WALLET_API_KEY=true` only when the report directory is controlled and you want the generator to replace the `api_key` placeholder with the first enabled MariaDB wallet API key. Set `FINAL_PLAN_USE_WALLET_SERVERKEY=true` to fill worker address-proof BasicAuth from `wallet.serverkey` values saved as `username:password`. Explicit `FINAL_PLAN_API_KEY`, `FINAL_PLAN_WORKER_USERNAME_*`, and `FINAL_PLAN_WORKER_PASSWORD_*` values always win over database lookups. Secret values also support `*_FILE`, including `FINAL_PLAN_API_KEY_FILE`, `FINAL_PLAN_ADMIN_PASSWORD_FILE`, `FINAL_PLAN_ADMIN_UPDATE_PASSWORD_FILE`, `FINAL_PLAN_WORKER_PASSWORD_FILE`, and per-worker `FINAL_PLAN_WORKER_PASSWORD_BNB_FILE` / `FINAL_PLAN_WORKER_PASSWORD_TRON_FILE`.

Run `/app/cutover-preflight` before `/app/final-plan` against the target MariaDB database. It is read-only and blocks when the Go main-service tables have not been created/imported yet, when required order-query indexes are missing, when `wallet` is missing, or when no cryptos are enabled. The report also records `order_index_rows` and `EXPLAIN` evidence for hot order-list paths so the final notes include database-level proof, not only HTTP latency:

```bash
docker run --rm --network shkeeper_default \
  -v /tmp/go-shkeeper-final-reports:/deploy-reports \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper" \
  -e CUTOVER_PREFLIGHT_OUTPUT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json \
  go-shkeeper /app/cutover-preflight
```

```bash
docker run --rm --network shkeeper_default \
  -v /tmp/go-shkeeper-final-reports:/deploy-reports \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper" \
  -e FINAL_PLAN_OUTPUT_FILE=/deploy-reports/go-shkeeper-final-cutover.plan.json \
  -e FINAL_PLAN_READINESS_FILE=/deploy-reports/go-shkeeper-final-readiness.json \
  go-shkeeper /app/final-plan

docker run --rm --network shkeeper_default \
  -v /tmp/go-shkeeper-final-reports:/deploy-reports \
  -e DEPLOY_CHECK_PLAN_FILE=/deploy-reports/go-shkeeper-final-cutover.plan.json \
  go-shkeeper /app/deploy-check
```

`FINAL_PLAN_READINESS_FILE` writes a small JSON readiness report beside the generated plan. It lists covered cryptos, workers, payment/payout counts, whether `order_status_matrix_check` is enabled, placeholder fields, missing coverage, warnings such as an absent standalone `order_external_id`, and booleans for whether the plan is ready for `deploy-check`, `cutover-audit`, and post-cutover verification. Readiness is blocked if the order status matrix check is disabled.

For worker address proof credentials, set `FINAL_PLAN_WORKER_USERNAME` and `FINAL_PLAN_WORKER_PASSWORD` when every worker shares the same BasicAuth pair. Override a specific worker with names such as `FINAL_PLAN_WORKER_USERNAME_BNB`, `FINAL_PLAN_WORKER_PASSWORD_BNB`, `FINAL_PLAN_WORKER_USERNAME_TRON`, or `FINAL_PLAN_WORKER_PASSWORD_TRON`.

## Legacy Main Data Import and Final Sync

The Go stack must be configured with `MARIADB_DATABASE_URL`; do not configure SQLite for the new main service or workers. The preferred final-sync path is MariaDB to MariaDB: point `LEGACY_MAIN_DATABASE_URL` at the old main-service MariaDB database and `MARIADB_DATABASE_URL` at the Go target database. Fresh deployments can skip this section and start with an empty MariaDB schema created by `/app/shkeeper`.

The final Go image does not contain Python or SQLite tools. `/app/import-legacy-main-mariadb` is a Go binary; it reads the source MariaDB schema, upserts rows into the target MariaDB database, and rebuilds `order_index`:

```bash
export MYSQL_ROOT_PASSWORD="$(docker exec mariadb printenv MYSQL_ROOT_PASSWORD)"

docker run --rm --network shkeeper_default \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper" \
  -e LEGACY_MAIN_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper_legacy" \
  -e IMPORT_LEGACY_MAIN_REPORT_FILE=/deploy-reports/go-shkeeper-main-import.json \
  -v /tmp/go-shkeeper-final-reports:/deploy-reports \
  go-shkeeper /app/import-legacy-main-mariadb
```

The import creates or migrates the target MariaDB tables first, preserves legacy primary keys for invoices, transactions, payouts, settings, wallet rows, and admin data, decodes byte strings from the old user table, and normalizes legacy minimum datetimes such as `0001-01-01 00:00:00.000000` to `NULL` for MariaDB strict-mode compatibility. The import mode is `mariadb-upsert`, so rerun it immediately before production cutover after stopping legacy writes; existing target rows are updated, `order_index` is rebuilt, and the report records imported table row counts plus `order_index_rows`.

`/app/import-legacy-json` remains available only for offline audit bundles or one-time recovery from an already exported legacy dump. It is not the default hk-16-16 rehearsal or final-sync path.

The old BNB sidecar stores generated address private keys in the MariaDB `bnb-shkeeper.wallets` table. Before replacing `bnb-shkeeper`, import those rows into the Go worker's shared `chain_account` table. Prefer the Go-native direct MariaDB import so the migration does not depend on a shell-generated JSON bridge:

```bash
docker run --rm --network shkeeper_default \
  -e CHAIN_MODULE=BNB \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper" \
  -e LEGACY_ACCOUNTS_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/bnb-shkeeper" \
  -e ACCOUNT_PASSWORD="$BNB_ACCOUNT_PASSWORD" \
  -e LEGACY_ACCOUNT_DECRYPT_URL="http://shkeeper:5000/api/v1/BNB/decrypt" \
  -e LEGACY_ACCOUNT_BACKEND_KEY="$SHKEEPER_BACKEND_KEY" \
  go-shkeeper /app/import-legacy-accounts
```

For audit trails or offline transfer, the same Go importer still accepts JSON:

```bash
docker exec mariadb mariadb -uroot -p"$MYSQL_ROOT_PASSWORD" -N -B bnb-shkeeper -e "
SELECT JSON_OBJECT(
  'tables',
  JSON_OBJECT(
    'wallets',
    COALESCE(JSON_ARRAYAGG(JSON_OBJECT(
      'pub_address', w.pub_address,
      'priv_key', w.priv_key,
      'crypto', COALESCE(a.crypto, IF(w.type = 'fee_deposit', 'BNB', 'BNB-USDT')),
      'type', w.type,
      'create_time', DATE_FORMAT(w.create_time, '%Y-%m-%d %H:%i:%s')
    )), JSON_ARRAY())
  )
)
FROM wallets w
LEFT JOIN accounts a ON a.address = w.pub_address;
" > /tmp/bnb-legacy-accounts.json

docker run --rm --network shkeeper_default \
  -v /tmp/bnb-legacy-accounts.json:/legacy-accounts.json:ro \
  -e CHAIN_MODULE=BNB \
  -e MARIADB_DATABASE_URL="mariadb://root:$MYSQL_ROOT_PASSWORD@mariadb:3306/shkeeper" \
  -e ACCOUNT_PASSWORD="$BNB_ACCOUNT_PASSWORD" \
  -e LEGACY_ACCOUNT_DECRYPT_URL="http://shkeeper:5000/api/v1/BNB/decrypt" \
  -e LEGACY_ACCOUNT_BACKEND_KEY="$SHKEEPER_BACKEND_KEY" \
  go-shkeeper /app/import-legacy-accounts /legacy-accounts.json
```

`LEGACY_ACCOUNTS_DATABASE_URL` points at the old sidecar MariaDB database and is read-only from the importer's perspective. `LEGACY_ACCOUNT_DECRYPT_URL` points at the old main service decrypt endpoint used by the old sidecar, and `LEGACY_ACCOUNT_BACKEND_KEY` is sent as `X-Shkeeper-Backend-Key`. On hk-16-16 the old Fernet key is supplied by that endpoint and is not necessarily the old BNB container's `ACCOUNT_PASSWORD` environment variable. If you already have the exact old Fernet password, pass `LEGACY_ACCOUNT_PASSWORD` or `LEGACY_ACCOUNT_PASSWORD_FILE` instead. `ACCOUNT_PASSWORD_FILE`, `LEGACY_ACCOUNTS_DATABASE_URL_FILE`, and `LEGACY_ACCOUNT_BACKEND_KEY_FILE` are also supported for avoiding secrets in command history; when using bind-mounted files, make them readable by the non-root `shkeeper` user in the Go image, or use Docker secrets' default read-only file mode. `/app/import-legacy-accounts` decrypts old Fernet secrets in Go and writes new `v1:` AES-GCM secrets for the Go worker.

If a legacy TRON sidecar has rows in `tron_keys`, export them with the same shape using `public` as the address, `private` as the encrypted or plain private key field, and `symbol` as the crypto. Then run `/app/import-legacy-accounts` with `CHAIN_MODULE=TRON`.

After collecting one or more `deploy-check` JSON reports, run `/app/cutover-audit` before replacing production containers:

```bash
docker run --rm \
  -v "$PWD/deploy-check.plan.json:/deploy-check.plan.json:ro" \
  -v "$PWD/deploy-reports:/deploy-reports:ro" \
  -e CUTOVER_AUDIT_PLAN_FILE=/deploy-check.plan.json \
  -e CUTOVER_AUDIT_REPORT_FILES=/deploy-reports/go-shkeeper-deploy-check-report.json \
  -e CUTOVER_AUDIT_IMPORT_REPORT_FILES=/deploy-reports/go-shkeeper-main-import-report.json \
  -e CUTOVER_AUDIT_PREFLIGHT_REPORT_FILES=/deploy-reports/go-shkeeper-cutover-preflight.json \
  -e CUTOVER_AUDIT_OUTPUT_FILE=/deploy-reports/go-shkeeper-cutover-audit.json \
  -e CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC=true \
  -e CUTOVER_AUDIT_REQUIRE_PREFLIGHT=true \
  -e CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true \
  -e CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS=true \
  -e CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID=true \
  go-shkeeper /app/cutover-audit
```

`cutover-audit` reads the plan `coverage_cryptos`/`cryptos` and worker names, then checks the deploy reports for `order_status_matrix`, `main_status`, `payment_order`, `payout_order`, and `worker_readyz` evidence. It exits nonzero and writes missing items when required proof is absent. For final production cutover, also pass `CUTOVER_AUDIT_IMPORT_REPORT_FILES` with `CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC=true` so the audit requires a fresh `/app/import-legacy-main-mariadb` report with `mode=mariadb-upsert`, imported rows, and rebuilt `order_index` rows. Pass `CUTOVER_AUDIT_PREFLIGHT_REPORT_FILES` with `CUTOVER_AUDIT_REQUIRE_PREFLIGHT=true` so required MariaDB tables, order-query indexes, enabled wallets, and hot-query `EXPLAIN` evidence are part of the acceptance artifact. For final modular-worker rehearsal, set `CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS=true`; this additionally requires a successful `worker_address` report entry for every expected worker. For final real-chain payout rehearsal, set `CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID=true`; this additionally requires a non-empty payout txid in the deploy-check report for every enabled chain/token.

After the replacement is live, collect the host container inventory and run `/app/post-cutover-verify`:

```bash
docker ps --format '{{json .}}' > deploy-reports/container-inventory.jsonl
docker run --rm --network shkeeper_default \
  -v "$PWD/deploy-reports:/deploy-reports:ro" \
  -e POST_CUTOVER_MAIN_URL=http://shkeeper:5000 \
  -e POST_CUTOVER_WORKER_URLS=btc=http://btc-worker:6000,ltc=http://ltc-worker:6000,doge=http://doge-worker:6000,firo=http://firo-worker:6000,lightning=http://btc-lightning-worker:6000,eth=http://eth-worker:6000,tron=http://tron-worker:6000,bnb=http://bnb-worker:6000,polygon=http://polygon-worker:6000,avalanche=http://avalanche-worker:6000,arbitrum=http://arbitrum-worker:6000,optimism=http://optimism-worker:6000,xmr=http://xmr-worker:6000,xrp=http://xrp-worker:6000,solana=http://solana-worker:6000 \
  -e POST_CUTOVER_DEPLOY_REPORT_FILES=/deploy-reports/go-shkeeper-deploy-check-report.json \
  -e POST_CUTOVER_AUDIT_FILE=/deploy-reports/go-shkeeper-cutover-audit.json \
  -e POST_CUTOVER_CONTAINER_INVENTORY_FILE=/deploy-reports/container-inventory.jsonl \
  -e POST_CUTOVER_EXPECTED_CONTAINERS=go-shkeeper=go-shkeeper,go-btc-worker=go-shkeeper,go-ltc-worker=go-shkeeper,go-doge-worker=go-shkeeper,go-firo-worker=go-shkeeper,go-btc-lightning-worker=go-shkeeper,go-eth-worker=go-shkeeper,go-tron-worker=go-shkeeper,go-bnb-worker=go-shkeeper,go-polygon-worker=go-shkeeper,go-avalanche-worker=go-shkeeper,go-arbitrum-worker=go-shkeeper,go-optimism-worker=go-shkeeper,go-solana-worker=go-shkeeper,go-xmr-worker=go-shkeeper,go-xrp-worker=go-shkeeper \
  -e POST_CUTOVER_FORBIDDEN_CONTAINERS=shkeeper,bnb-shkeeper,bnb_tasks,tron-shkeeper,tron_tasks \
  -e POST_CUTOVER_EXPECTED_IMAGES=go-shkeeper \
  -e POST_CUTOVER_FORBIDDEN_IMAGES=ghcr.io/sky-jd/shkeeper.io-zh,vsyshost/bnb-shkeeper,vsyshost/tron-shkeeper,python: \
  -e POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX=true \
  -e POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID=true \
  -e POST_CUTOVER_OUTPUT_FILE=/deploy-reports/go-shkeeper-post-cutover.json \
  go-shkeeper /app/post-cutover-verify
```

The post-cutover verifier fails when the live main service or workers are not ready, any deploy report failed, the cutover audit did not pass, the cutover audit did not prove `order_status_matrix`, a required Go image is absent from the inventory, a required container name is absent or running the wrong image, or forbidden legacy container names/images are still running. `POST_CUTOVER_EXPECTED_CONTAINERS` accepts comma-separated `name=image-substring` entries; omit `=image-substring` when only the container name matters. Keep `POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID=true` for final real-chain payout acceptance so every covered crypto must have a payout txid in the audit.

## Services

- `shkeeper`: Go main API/admin/callback service.
- `btc-worker`: Go chain worker for BTC sidecar-compatible endpoints backed by Bitcoin Core JSON-RPC.
- `ltc-worker`: Go chain worker for LTC endpoints backed by Litecoin JSON-RPC.
- `doge-worker`: Go chain worker for DOGE endpoints backed by Dogecoin JSON-RPC.
- `firo-worker`: Go chain worker for FIRO and FIRO-SPARK endpoints backed by Firo JSON-RPC.
- `btc-lightning-worker`: Go chain worker for BTC Lightning endpoints backed by LND REST.
- `eth-worker`: Go chain worker for ETH and Ethereum ERC20 module endpoints.
- `tron-worker`: Go chain worker for TRX, USDT, and USDC module endpoints.
- `bnb-worker`: Go chain worker for BNB and BNB token module endpoints.
- `polygon-worker`: Go chain worker for MATIC and Polygon token module endpoints.
- `avalanche-worker`: Go chain worker for AVAX and Avalanche token module endpoints.
- `arbitrum-worker`: Go chain worker for Arbitrum ETH and token module endpoints.
- `optimism-worker`: Go chain worker for Optimism ETH and token module endpoints.
- `solana-worker`: Go chain worker for SOL and SPL token module endpoints.
- `xmr-worker`: Go chain worker for XMR endpoints backed by Monero daemon RPC and `monero-wallet-rpc`.
- `xrp-worker`: Go chain worker for XRP endpoints backed by rippled JSON-RPC.
- `mariadb`: shared MariaDB database.
- `redis`: reserved for future async queue compatibility; the current Go workers persist tasks in MariaDB.

## Current Worker Coverage

The Go chain worker already provides:

- Basic-auth compatible module endpoints.
- MariaDB schema for generated chain accounts and async tasks.
- AES-GCM encrypted private-key storage using `ACCOUNT_PASSWORD`.
- BTC status, balance, address generation, transaction lookup, fee estimation, fee deposit account, payout, multipayout, and task records through Bitcoin Core JSON-RPC.
- Modular worker endpoints for LTC, DOGE, FIRO, and FIRO-SPARK. FIRO-SPARK uses Spark-specific RPC calls instead of ordinary `sendtoaddress`.
- BTC Lightning status, channel balance, invoice generation, invoice lookup by `r_hash`, payout through payment requests or LNURL via LNbits, fee deposit LNURL, multipayout, and task records.
- BNB and TRON address generation.
- BNB/TRON fullnode status probing.
- BNB native and BEP20 balance aggregation for generated accounts.
- TRX and TRC20 balance aggregation for generated accounts.
- Local private-key signing and broadcast for BNB, BEP20, TRX, and TRC20 payouts.
- TRON fee-deposit account, multiserver status/change, account resource inspection, and `freezebalancev2` staking endpoints for the old TRON admin workflow.
- Native and token `Transfer` parsing through `/{crypto}/transaction/{txid}` for payout confirmation polling and wallet notifications.
- Payout task records compatible with the main API polling flow.
- XMR status, wallet balance, address generation, address listing, transaction lookup, fee deposit account, payout, and task records.
- XRP status, wallet balance, X-address generation with destination tags, address listing, transaction lookup, payout submit, multipayout, and task records.
- Solana status, SOL/SPL balance aggregation, ed25519 address generation, ATA-aware SOL/SPL payouts, transaction lookup, multipayout, and task records. `SOLANA-USDT` and `SOLANA-USDC` use SPL Token; `SOLANA-PYUSD` uses Token-2022 by default.
- Generic EVM worker support for `CHAIN_MODULE=ETH`, `MATIC`, `AVAX`, `ARBETH`, and `OPETH`. Configure `FULLNODE_URL`, `EVM_CHAIN_ID`, auth variables, and token `*_CONTRACT` plus `*_DECIMALS` values per worker.

The full hk modular compose default `SHKEEPER_CRYPTOS` list is kept in sync with the legacy `shkeeper/modules/cryptos/*.py` modules by Go tests, so adding or removing a legacy crypto file will fail CI until the Go definitions and full deployment list are updated together. The smaller top-level `docker-compose.example.yml` remains a compact starter stack; use `hk-16-16.modular.example.yml` for full replacement coverage.

Before replacing the old production sidecar images, run small-value real-chain tests for every enabled token contract or mint/fullnode pair and configure token contracts, Solana mint/program overrides, Lightning LND macaroon/TLS settings, TRON fullnode/multiserver and fee-limit overrides, TRON staking account overrides when needed, XMR daemon/wallet RPC credentials, and XRP account/secret settings in the compose environment.
