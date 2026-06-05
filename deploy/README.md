# Go SHKeeper deployment

`modular.example.yml` is the MariaDB-only modular layout for replacing a legacy SHKeeper main service and chain sidecar containers with Go binaries from this project. SQLite URLs and SQLite files are not supported by the Go service or workers.

## Remote source upgrade

Use `remote-source-upgrade.sh` when a deployment host should receive the current local checkout as a source archive and rebuild the Go stack in place. It preserves runtime-only files such as `.env`, `docker-compose.example.yml`, `secrets`, `deploy-reports`, and the installed manager script.

```bash
GO_SHKEEPER_REMOTE_HOST=go-shkeeper-host \
GO_SHKEEPER_REMOTE_DIR=/opt/go-shkeeper \
bash deploy/remote-source-upgrade.sh
```

The remote upgrade runs `deploy/shkeeperctl.sh source-upgrade`, waits for `/readyz`, and verifies that the rebuilt `/app/shkeeper` binary contains the admin Vue API markers.

## MariaDB-only staging rehearsal

Run `staging-rehearsal.sh` from a repository checkout on the deployment host before any production replacement. It builds the Go image when needed, creates an isolated MariaDB database, imports the old main data through `/app/import-legacy-main-mariadb`, imports legacy BNB accounts through `/app/import-legacy-accounts` when the old backend key is available, starts temporary Go main/BNB/TRON containers, runs payment/admin/order-list checks, and finishes with `/app/cutover-audit` plus a non-production `/app/release-audit`.

```bash
bash go-shkeeper/deploy/staging-rehearsal.sh
```

Set `GO_SHKEEPER_IMAGE=go-shkeeper:tag BUILD_IMAGE=0` to reuse an already built image. Set `KEEP_REHEARSAL_REPORTS=1` to keep rehearsal reports or `KEEP_REHEARSAL_DB=1` to keep the temporary MariaDB database for manual inspection.

The rehearsal and production gates include `CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC`, `CUTOVER_AUDIT_REQUIRE_PREFLIGHT`, `CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX`, `POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX`, and `POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID`.

## Final readiness

Use `final-readiness.sh` as the final readiness step to generate the MariaDB-backed plan and readiness report. It builds or reuses the Go image, writes `/app/runtime-audit` to `runtime-audit.json`, records `container-inventory.jsonl`, runs read-only `/app/cutover-preflight`, runs `/app/final-plan`, writes the plan plus readiness JSON into `REPORT_DIR`, and then writes a `goal-audit report`. It does not stop production containers.

```bash
API_KEY_FILE=/run/secrets/go_shkeeper_api_key \
ADMIN_PASSWORD_FILE=/run/secrets/go_shkeeper_admin_password \
WORKER_PASSWORD_FILE=/run/secrets/go_shkeeper_worker_password \
FINAL_PLAN_USE_WALLET_API_KEY=true \
bash go-shkeeper/deploy/final-readiness.sh
```

Database-write and real-chain paths are guarded:

```bash
UPDATE_WORKER_SERVERKEY=1 CONFIRM_DB_WRITE=GO_SHKEEPER_PRODUCTION \
bash go-shkeeper/deploy/final-readiness.sh

RUN_DEPLOY_CHECK=1 CONFIRM_REAL_CHAIN_REHEARSAL=GO_SHKEEPER_PRODUCTION \
bash go-shkeeper/deploy/final-readiness.sh
```

When SSH sessions are unstable, start the same readiness flow through `async-final-readiness.sh`. It writes a detached job, log, pid file, and status file under `REPORT_DIR`, then returns immediately so a dropped SSH session does not kill the Docker build or readiness report generation. Use `SOURCE_ARCHIVE=/tmp/go-shkeeper-src.tgz` when running from an uploaded source archive. The async wrapper refuses inline secret values by default; pass `API_KEY_FILE`, `ADMIN_PASSWORD_FILE`, `ADMIN_UPDATE_PASSWORD_FILE`, and worker password file paths instead.

```bash
bash go-shkeeper/deploy/async-final-readiness.sh
tail -f /tmp/go-async-readiness-report/async-final-readiness.log
cat /tmp/go-async-readiness-report/async-final-readiness.status
```

## Production cutover

`production-cutover.sh` is the guarded production entrypoint. It defaults to `DRY_RUN=1`; it refuses to stop legacy containers unless `DRY_RUN=0 CONFIRM_PRODUCTION_CUTOVER=GO_SHKEEPER_PRODUCTION` is set. The script requires a completed final deploy-check plan, deploy-check report, and final readiness report, stops legacy write containers, runs the final `/app/import-legacy-main-mariadb` sync, runs `/app/cutover-preflight`, runs strict `/app/cutover-audit` with import/preflight/order-matrix/worker-address/payout-txid gates, starts the Go modular compose services, writes `container-inventory.jsonl` and `container-stats.jsonl`, runs `/app/post-cutover-verify`, writes `/app/release-audit`, and then runs `/app/goal-audit` as the final objective-level acceptance report. If any step after stopping legacy containers fails, including `goal-audit`, it removes Go containers and restarts the legacy containers by default.

```bash
PLAN_FILE=/deploy-reports/go-shkeeper-final/go-shkeeper-final-cutover.plan.json \
FINAL_DEPLOY_REPORT_FILE=/deploy-reports/go-shkeeper-final/go-shkeeper-final-deploy-check-report.json \
FINAL_READINESS_REPORT_FILE=/deploy-reports/go-shkeeper-final/go-shkeeper-final-readiness.json \
DRY_RUN=0 CONFIRM_PRODUCTION_CUTOVER=GO_SHKEEPER_PRODUCTION \
bash go-shkeeper/deploy/production-cutover.sh
```

Post-cutover examples should match `modular.example.yml` container names:

```bash
docker run --rm --network shkeeper_default \
  -e POST_CUTOVER_EXPECTED_CONTAINERS=go-shkeeper=go-shkeeper,go-btc-worker=go-shkeeper,go-ltc-worker=go-shkeeper,go-doge-worker=go-shkeeper,go-firo-worker=go-shkeeper,go-btc-lightning-worker=go-shkeeper,go-eth-worker=go-shkeeper,go-tron-worker=go-shkeeper,go-bnb-worker=go-shkeeper,go-polygon-worker=go-shkeeper,go-avalanche-worker=go-shkeeper,go-arbitrum-worker=go-shkeeper,go-optimism-worker=go-shkeeper,go-solana-worker=go-shkeeper,go-xmr-worker=go-shkeeper,go-xrp-worker=go-shkeeper \
  -e POST_CUTOVER_FORBIDDEN_CONTAINERS=shkeeper,bnb-shkeeper,bnb_tasks,tron-shkeeper,tron_tasks \
  -e POST_CUTOVER_WORKER_URLS=http://btc-worker:6000,http://ltc-worker:6000,http://doge-worker:6000,http://firo-worker:6000,http://btc-lightning-worker:6000,http://eth-worker:6000,http://tron-worker:6000,http://bnb-worker:6000,http://polygon-worker:6000,http://avalanche-worker:6000,http://arbitrum-worker:6000,http://optimism-worker:6000,http://solana-worker:6000,http://xmr-worker:6000,http://xrp-worker:6000 \
  -e POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX=true \
  -e POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID=true \
  go-shkeeper:tag /app/post-cutover-verify
```

## Deploy-check plans

Set `DEPLOY_CHECK_REPORT_FILE` or `report_file` in a plan to keep a machine-readable cutover artifact. Reports are written on both success and failure and include each `ok`, `skipped`, and `failed` check with timestamps, crypto names, external IDs, order list rows, concurrency/latency fields, and the final error.

For read-only order completeness proof, set `DEPLOY_CHECK_ORDER_STATUS_MATRIX=true`. The verifier pages through `/api/v1/orders`, discovers invoice/payout status plus crypto pairs from the response, and then verifies every pair through the filtered list API. Use `DEPLOY_CHECK_ORDER_STATUS_MATRIX_EXPECT_STATUSES=PAID,UNPAID` and `DEPLOY_CHECK_ORDER_STATUS_MATRIX_MIN=2` when validating imported data that should contain both paid and unpaid orders. The matrix intentionally ignores nested transaction statuses such as `CONFIRMED`, because the order-list filter applies to invoice and payout states.

`final-cutover.plan.template.json` is the strict final rehearsal template. It sets `cryptos` and `coverage_cryptos` to the full default modular list, enables `require_payment_coverage`, enables `require_payout_coverage`, enables `order_status_matrix_check`, includes a payment and payout check for every chain/token, and includes one address-generation proof for every worker. `deploy-check` runs the order status matrix after the mutating payment/payout probes, so the matrix must see the generated full-coverage order evidence. Each payout uses `destination_from_payment` to send to the wallet or payment request created by the matching payment check, so the final rehearsal does not require preparing external destination addresses. Copy the template outside the repository, replace every `replace-with-*` value with the live API key, admin credential, worker BasicAuth credentials, and intentional small-value payout amounts, then run it with `/app/deploy-check`. If `mutating=true` and any placeholder remains, `deploy-check` fails immediately with `plan_placeholders` before making HTTP requests or dispatching payouts.

The image also includes `/app/final-plan`, a Go-native plan generator. It can read the MariaDB `wallet` table and generate a narrower final rehearsal plan for the currently enabled wallet cryptos, or generate the full default modular matrix with `FINAL_PLAN_ALL_CRYPTOS=true`. It still leaves credentials and payout amounts as placeholders unless supplied through environment variables, so `deploy-check` will stop at `plan_placeholders` until the plan is intentionally completed.

`/app/import-legacy-json` remains available only for offline audit bundles or one-time recovery from an already exported legacy dump. It is not the default rehearsal or final-sync path.

`LEGACY_ACCOUNTS_DATABASE_URL` points at the old sidecar MariaDB database and is read-only from the importer's perspective. `LEGACY_ACCOUNT_DECRYPT_URL` points at the old main service decrypt endpoint used by the old sidecar, and `LEGACY_ACCOUNT_BACKEND_KEY` is sent as `X-Shkeeper-Backend-Key`. If you already have the exact old Fernet password, pass `LEGACY_ACCOUNT_PASSWORD` or `LEGACY_ACCOUNT_PASSWORD_FILE` instead. `ACCOUNT_PASSWORD_FILE`, `LEGACY_ACCOUNTS_DATABASE_URL_FILE`, and `LEGACY_ACCOUNT_BACKEND_KEY_FILE` are also supported for avoiding secrets in command history; when using bind-mounted files, make them readable by the non-root `shkeeper` user in the Go image, or use Docker secrets' default read-only file mode. `/app/import-legacy-accounts` decrypts old Fernet secrets in Go and writes new `v1:` AES-GCM secrets for the Go worker.

The full modular compose default `SHKEEPER_CRYPTOS` list is kept in sync with the legacy `shkeeper/modules/cryptos/*.py` modules by Go tests, so adding or removing a legacy crypto file will fail CI until the Go definitions and full deployment list are updated together. The smaller top-level `docker-compose.example.yml` remains a compact starter stack; use `modular.example.yml` for full replacement coverage.
