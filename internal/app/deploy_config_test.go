package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHKModularComposeCoversLegacyEVMWorkers(t *testing.T) {
	body, err := os.ReadFile("../../deploy/hk-16-16.modular.example.yml")
	if err != nil {
		t.Fatalf("read modular compose: %v", err)
	}
	text := string(body)

	requiredServices := []string{
		"eth-worker:",
		"polygon-worker:",
		"avalanche-worker:",
		"arbitrum-worker:",
		"optimism-worker:",
	}
	for _, service := range requiredServices {
		if !strings.Contains(text, "\n  "+service) {
			t.Fatalf("modular compose is missing service %s", service)
		}
	}

	requiredMainEnv := []string{
		`ETHEREUM_API_SERVER_HOST: "eth-worker"`,
		`POLYGON_API_SERVER_HOST: "polygon-worker"`,
		`AVALANCHE_API_SERVER_HOST: "avalanche-worker"`,
		`ARBITRUM_API_SERVER_HOST: "arbitrum-worker"`,
		`OPTIMISM_API_SERVER_HOST: "optimism-worker"`,
	}
	for _, envLine := range requiredMainEnv {
		if !strings.Contains(text, envLine) {
			t.Fatalf("modular compose is missing main-service env %s", envLine)
		}
	}

	for _, crypto := range []string{
		"ETH", "ETH-USDT", "ETH-USDC", "ETH-PYUSD",
		"MATIC", "POLYGON-USDT", "POLYGON-USDC",
		"AVAX", "AVALANCHE-USDT", "AVALANCHE-USDC",
		"ARBETH", "ARB-USDC", "ARB-PYUSD", "ARB-TOKEN",
		"OPETH", "OP-USDT", "OP-USDC", "OP-TOKEN",
	} {
		if !strings.Contains(text, crypto) {
			t.Fatalf("modular compose is missing legacy EVM crypto %s", crypto)
		}
	}
}

func TestComposeCoversBitcoinLikeWorkers(t *testing.T) {
	files := []string{
		"../../docker-compose.example.yml",
		"../../deploy/hk-16-16.modular.example.yml",
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read compose file %s: %v", file, err)
		}
		text := string(body)
		required := []string{
			"ltc-worker:",
			"doge-worker:",
			"firo-worker:",
			`CHAIN_MODULE: "LTC"`,
			`CHAIN_MODULE: "DOGE"`,
			`CHAIN_MODULE: "FIRO"`,
			`LTC_API_SERVER_HOST: "ltc-worker"`,
			`DOGE_API_SERVER_HOST: "doge-worker"`,
			`FIRO_API_SERVER_HOST: "firo-worker"`,
			"FIRO-SPARK",
		}
		for _, item := range required {
			if !strings.Contains(text, item) {
				t.Fatalf("%s is missing Bitcoin-like modular worker config %s", file, item)
			}
		}
	}
}

func TestComposeExamplesUseReadyHealthchecks(t *testing.T) {
	files := []string{
		"../../docker-compose.example.yml",
		"../../deploy/hk-16-16.modular.example.yml",
		"../../deploy/evm-worker.example.yml",
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read compose file %s: %v", file, err)
		}
		text := string(body)
		if !strings.Contains(text, "healthcheck:") {
			t.Fatalf("%s is missing healthcheck entries", file)
		}
		if !strings.Contains(text, "/readyz") {
			t.Fatalf("%s healthchecks must use /readyz", file)
		}
	}
}

func TestDeployConfigsUseExplicitMariaDBURL(t *testing.T) {
	files := []string{
		"../../docker-compose.example.yml",
		"../../deploy/hk-16-16.modular.example.yml",
		"../../deploy/evm-worker.example.yml",
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read deploy config %s: %v", file, err)
		}
		text := string(body)
		if strings.Contains(text, "\n      DATABASE_URL:") || strings.Contains(text, "-e DATABASE_URL=") {
			t.Fatalf("%s should use MARIADB_DATABASE_URL for runtime database configuration", file)
		}
		if !strings.Contains(text, "MARIADB_DATABASE_URL") {
			t.Fatalf("%s is missing explicit MARIADB_DATABASE_URL runtime configuration", file)
		}
	}
}

func TestHKStagingRehearsalUsesMariaDBAndCutoverGates(t *testing.T) {
	body, err := os.ReadFile("../../deploy/hk-16-16-staging-rehearsal.sh")
	if err != nil {
		t.Fatalf("read hk staging rehearsal script: %v", err)
	}
	text := string(body)
	required := []string{
		"MYSQL_ROOT_PASSWORD",
		"MARIADB_HOST",
		`MARIADB_DATABASE_URL="mariadb://`,
		"/app/runtime-audit",
		"/app/import-legacy-main-mariadb",
		"/app/import-legacy-accounts",
		"/app/admin-account",
		"/app/worker-serverkey",
		"/app/deploy-check",
		"/app/cutover-audit",
		"/app/release-audit",
		`chmod 0777 "$WORK_DIR"`,
		"KEEP_REHEARSAL_DB",
		"KEEP_REHEARSAL_REPORTS",
		"REHEARSAL_ORDER_LIST_REQUESTS",
		"REHEARSAL_ORDER_LIST_CONCURRENCY",
		"REHEARSAL_ORDER_LIST_MAX_LATENCY_MS",
		"REHEARSAL_READY_REQUESTS",
		"REHEARSAL_READY_CONCURRENCY",
		"cutover-preflight.json",
		"release-audit.json",
		"container-stats.jsonl",
		"bnb-serverkey-report.json",
		"tron-serverkey-report.json",
		`"order_status_matrix_check": true`,
		`"order_status_matrix_expect_statuses": ["PAID", "UNPAID"]`,
		`"order_list_requests": $REHEARSAL_ORDER_LIST_REQUESTS`,
		`"order_list_concurrency": $REHEARSAL_ORDER_LIST_CONCURRENCY`,
		`"requests": $REHEARSAL_READY_REQUESTS`,
		`"concurrency": $REHEARSAL_READY_CONCURRENCY`,
		"CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC=true",
		"CUTOVER_AUDIT_REQUIRE_PREFLIGHT=true",
		"CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true",
		"RELEASE_AUDIT_REQUIRE_POST_CUTOVER=false",
		"RELEASE_AUDIT_REQUIRE_PAYOUT_COVERAGE=false",
		"RELEASE_AUDIT_REQUIRE_PAYOUT_ORDER=false",
		"RELEASE_AUDIT_REQUIRE_PAYOUT_TXID=false",
		`"coverage_cryptos": ["BNB-USDT", "TRX"]`,
		`"require_payment_coverage": true`,
		`"crypto": "BNB-USDT"`,
		`"crypto": "TRX"`,
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("hk staging rehearsal script is missing %q", item)
		}
	}
	forbidden := []string{
		`-e DATABASE_URL=`,
		`DATABASE_URL="sqlite`,
		`sqlite://`,
		`python3`,
		`sqlite3`,
	}
	for _, item := range forbidden {
		if strings.Contains(text, item) {
			t.Fatalf("hk staging rehearsal script must not use %q", item)
		}
	}

	readmeBody, err := os.ReadFile("../../deploy/README.md")
	if err != nil {
		t.Fatalf("read deploy README: %v", err)
	}
	readme := string(readmeBody)
	for _, item := range []string{
		"hk-16-16-staging-rehearsal.sh",
		"MariaDB-only staging rehearsal",
		"non-production `/app/release-audit`",
		"KEEP_REHEARSAL_REPORTS=1",
		"KEEP_REHEARSAL_DB=1",
		"CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC",
		"CUTOVER_AUDIT_REQUIRE_PREFLIGHT",
		"CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX",
		"POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX",
		"POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID",
	} {
		if !strings.Contains(readme, item) {
			t.Fatalf("deploy README is missing hk rehearsal documentation marker %q", item)
		}
	}
}

func TestHKProductionCutoverScriptIsGuardedAndAudited(t *testing.T) {
	body, err := os.ReadFile("../../deploy/hk-16-16-production-cutover.sh")
	if err != nil {
		t.Fatalf("read hk production cutover script: %v", err)
	}
	text := string(body)
	required := []string{
		"DRY_RUN=\"${DRY_RUN:-1}\"",
		"CONFIRM_PRODUCTION_CUTOVER",
		"GO_SHKEEPER_HK_16_16",
		"ROLLBACK_ON_FAILURE",
		"rollback_needed=1",
		"production_cutover_dry_run_ok",
		"UTILITY_DOCKER_USER",
		`--user "$UTILITY_DOCKER_USER"`,
		"docker start \"$container\"",
		"/app/runtime-audit",
		"runtime-audit.json",
		"/app/import-legacy-main-mariadb",
		"/app/cutover-preflight",
		"/app/cutover-audit",
		"/app/post-cutover-verify",
		"/app/release-audit",
		"/app/goal-audit",
		"FINAL_READINESS_REPORT_FILE",
		"CUTOVER_AUDIT_REQUIRE_IMPORT_SYNC=true",
		"CUTOVER_AUDIT_REQUIRE_PREFLIGHT=true",
		"CUTOVER_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true",
		"CUTOVER_AUDIT_REQUIRE_WORKER_ADDRESS=true",
		"CUTOVER_AUDIT_REQUIRE_PAYOUT_TXID=true",
		"POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX=true",
		"POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID=true",
		"RELEASE_AUDIT_REQUIRE_ORDER_STATUS_MATRIX=true",
		"RELEASE_AUDIT_REQUIRE_PAYMENT_COVERAGE=true",
		"RELEASE_AUDIT_REQUIRE_PAYOUT_COVERAGE=true",
		"RELEASE_AUDIT_REQUIRE_PAYMENT_ORDER=true",
		"RELEASE_AUDIT_REQUIRE_PAYOUT_ORDER=true",
		"RELEASE_AUDIT_REQUIRE_PAYOUT_TXID=true",
		"RELEASE_AUDIT_REQUIRE_POST_CUTOVER=true",
		"RELEASE_AUDIT_REQUIRE_WORKER_READY=true",
		"GOAL_AUDIT_RUNTIME_AUDIT_FILE=/deploy-reports/runtime-audit.json",
		"GOAL_AUDIT_PREFLIGHT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json",
		"GOAL_AUDIT_READINESS_FILE=/deploy-reports/go-shkeeper-final-readiness.json",
		"GOAL_AUDIT_DEPLOY_REPORT_FILES=/deploy-reports/go-shkeeper-final-deploy-check-report.json",
		"GOAL_AUDIT_RELEASE_AUDIT_FILE=/deploy-reports/go-shkeeper-release-audit.json",
		"GOAL_AUDIT_POST_CUTOVER_FILE=/deploy-reports/go-shkeeper-post-cutover.json",
		"GOAL_AUDIT_CONTAINER_INVENTORY_FILE=/deploy-reports/container-inventory.jsonl",
		"GOAL_AUDIT_CONTAINER_STATS_FILE=/deploy-reports/container-stats.jsonl",
		"GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB",
		"GOAL_AUDIT_OUTPUT_FILE=/deploy-reports/go-shkeeper-goal-audit.json",
		"CONTAINER_STATS_FILE",
		"docker stats --no-stream",
		"docker compose -f",
		"up -d --no-deps",
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("hk production cutover script is missing %q", item)
		}
	}
	forbidden := []string{
		"sqlite://",
		"sqlite3",
		"python3",
		"DROP DATABASE",
		"GOAL_AUDIT_REQUIRE_STRICT_RELEASE_GATES=false",
	}
	for _, item := range forbidden {
		if strings.Contains(text, item) {
			t.Fatalf("hk production cutover script must not use %q", item)
		}
	}

	readmeBody, err := os.ReadFile("../../deploy/README.md")
	if err != nil {
		t.Fatalf("read deploy README: %v", err)
	}
	readme := string(readmeBody)
	for _, item := range []string{
		"hk-16-16-production-cutover.sh",
		"DRY_RUN=0 CONFIRM_PRODUCTION_CUTOVER=GO_SHKEEPER_HK_16_16",
		"restarts the legacy containers",
		"/app/goal-audit",
	} {
		if !strings.Contains(readme, item) {
			t.Fatalf("deploy README is missing production cutover marker %q", item)
		}
	}
}

func TestHKFinalReadinessScriptIsMariaDBOnlyAndGuarded(t *testing.T) {
	body, err := os.ReadFile("../../deploy/hk-16-16-final-readiness.sh")
	if err != nil {
		t.Fatalf("read hk final readiness script: %v", err)
	}
	text := string(body)
	required := []string{
		"RUN_DEPLOY_CHECK=\"${RUN_DEPLOY_CHECK:-0}\"",
		"UPDATE_WORKER_SERVERKEY=\"${UPDATE_WORKER_SERVERKEY:-0}\"",
		"CONFIRM_REAL_CHAIN_REHEARSAL",
		"CONFIRM_DB_WRITE",
		"GO_SHKEEPER_HK_16_16",
		"MARIADB_DATABASE_URL",
		`mariadb://root:$MYSQL_ROOT_PASSWORD@`,
		"require_mariadb_url",
		"UTILITY_DOCKER_USER",
		`--user "$UTILITY_DOCKER_USER"`,
		"/app/runtime-audit",
		"/app/cutover-preflight",
		"/app/final-plan",
		"/app/worker-serverkey",
		"/app/deploy-check",
		"/app/goal-audit",
		"RUNTIME_AUDIT_REPORT",
		"GOAL_AUDIT_REPORT",
		"INVENTORY_FILE",
		"runtime-audit.json",
		"goal-audit.json",
		"container-inventory.jsonl",
		"GOAL_AUDIT_RUNTIME_AUDIT_FILE=/deploy-reports/runtime-audit.json",
		"GOAL_AUDIT_PREFLIGHT_FILE=/deploy-reports/go-shkeeper-cutover-preflight.json",
		"GOAL_AUDIT_READINESS_FILE=/deploy-reports/go-shkeeper-final-readiness.json",
		"GOAL_AUDIT_DEPLOY_REPORT_FILES=/deploy-reports/go-shkeeper-final-deploy-check-report.json",
		"GOAL_AUDIT_CONTAINER_INVENTORY_FILE=/deploy-reports/container-inventory.jsonl",
		"GOAL_AUDIT_CONTAINER_STATS_FILE=/deploy-reports/container-stats.jsonl",
		"GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB",
		"GOAL_AUDIT_OUTPUT_FILE=/deploy-reports/goal-audit.json",
		"FINAL_PLAN_API_KEY_FILE",
		"FINAL_PLAN_ADMIN_PASSWORD_FILE",
		"FINAL_PLAN_ADMIN_UPDATE_PASSWORD_FILE",
		"FINAL_PLAN_WORKER_PASSWORD_FILE",
		"DEPLOY_CHECK_API_KEY_FILE",
		"DEPLOY_CHECK_ADMIN_PASSWORD_FILE",
		"DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD_FILE",
		"DEPLOY_CHECK_WORKER_PASSWORD_FILE",
		"DEPLOY_CHECK_PAYOUT_AMOUNT",
		"FINAL_PLAN_USE_WALLET_API_KEY",
		"FINAL_PLAN_USE_WALLET_SERVERKEY",
		"FINAL_PLAN_REDACT_SECRETS",
		"final_readiness_blocked",
		"deploy_check_skipped",
		`chmod 0777 "$REPORT_DIR"`,
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("hk final readiness script is missing %q", item)
		}
	}
	forbidden := []string{
		`-e DATABASE_URL=`,
		`DATABASE_URL="sqlite`,
		"sqlite://",
		"sqlite3",
		"python3",
		"docker stop",
		"docker compose",
		"CONFIRM_PRODUCTION_CUTOVER",
		"GOAL_AUDIT_REQUIRE_STRICT_RELEASE_GATES=false",
	}
	for _, item := range forbidden {
		if strings.Contains(text, item) {
			t.Fatalf("hk final readiness script must not use %q", item)
		}
	}

	readmeBody, err := os.ReadFile("../../deploy/README.md")
	if err != nil {
		t.Fatalf("read deploy README: %v", err)
	}
	readme := string(readmeBody)
	for _, item := range []string{
		"hk-16-16-final-readiness.sh",
		"final readiness",
		"does not stop production containers",
		"RUN_DEPLOY_CHECK=1 CONFIRM_REAL_CHAIN_REHEARSAL=GO_SHKEEPER_HK_16_16",
		"UPDATE_WORKER_SERVERKEY=1 CONFIRM_DB_WRITE=GO_SHKEEPER_HK_16_16",
		"ADMIN_PASSWORD_FILE",
		"WORKER_PASSWORD_FILE",
		"FINAL_PLAN_USE_WALLET_API_KEY=true",
		"goal-audit report",
	} {
		if !strings.Contains(readme, item) {
			t.Fatalf("deploy README is missing final readiness marker %q", item)
		}
	}
}

func TestGitHubWorkflowRunsMariaDBAndScansAllDeployEntrypoints(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/go-shkeeper.yml")
	if err != nil {
		t.Fatalf("read GitHub workflow: %v", err)
	}
	text := string(body)
	required := []string{
		"mariadb:11.4",
		"GO_SHKEEPER_TEST_DATABASE_URL: mariadb://",
		"go test -p 1 ./...",
		"contrib/shkeeper-change-password.sh",
		"Reject SQLite and Python dependencies",
		"Reject active SQLite and Python source",
		"deploy/hk-16-16-staging-rehearsal.sh",
		"deploy/hk-16-16-production-cutover.sh",
		"deploy/hk-16-16-final-readiness.sh",
		"deploy/hk-16-16-async-final-readiness.sh",
		"deploy/hk-16-16.modular.example.yml",
		"deploy/evm-worker.example.yml",
		"/app/runtime-audit",
		"/app/cutover-preflight",
		"/app/goal-audit",
		"/tmp/go-shkeeper-cutover-preflight.json",
		"docker stats --no-stream",
		"GOAL_AUDIT_CONTAINER_STATS_FILE=/deploy-reports/go-shkeeper-container-stats.jsonl",
		"GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB=512",
		"GOAL_AUDIT_REQUIRE_STRICT_RELEASE_GATES=false",
		`grep -q '"status": "pass"' /tmp/go-shkeeper-goal-audit.json`,
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("go-shkeeper workflow is missing %q", item)
		}
	}
}

func TestHKAsyncFinalReadinessScriptSurvivesSSHDisconnectsAndAvoidsInlineSecrets(t *testing.T) {
	body, err := os.ReadFile("../../deploy/hk-16-16-async-final-readiness.sh")
	if err != nil {
		t.Fatalf("read hk async final readiness script: %v", err)
	}
	text := string(body)
	required := []string{
		"nohup bash",
		"async-final-readiness.log",
		"async-final-readiness.status",
		"async-final-readiness.pid",
		"SOURCE_ARCHIVE",
		"CLEANUP_SECRET_DIRS",
		"cleanup_secret_dirs",
		"/tmp/codex-*secret*",
		"Refusing cleanup of non-codex secret path",
		"hk-16-16-final-readiness.sh",
		"ALLOW_INLINE_SECRETS",
		"Refusing inline secret env",
		"API_KEY_FILE",
		"ADMIN_PASSWORD_FILE",
		"WORKER_PASSWORD_FILE",
		"FINAL_PLAN_REDACT_SECRETS",
		"UTILITY_DOCKER_USER",
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("hk async final readiness script is missing %q", item)
		}
	}
	forbidden := []string{
		"sqlite://",
		"sqlite3",
		"python3",
		"docker stop",
		"docker compose",
		"CONFIRM_PRODUCTION_CUTOVER",
	}
	for _, item := range forbidden {
		if strings.Contains(text, item) {
			t.Fatalf("hk async final readiness script must not use %q", item)
		}
	}
}

func TestContribPasswordHelperUsesGoAdminAccountAndMariaDB(t *testing.T) {
	body, err := os.ReadFile("../../contrib/shkeeper-change-password.sh")
	if err != nil {
		t.Fatalf("read password helper: %v", err)
	}
	text := string(body)
	required := []string{
		"#!/usr/bin/env bash",
		"/app/admin-account",
		"GO_SHKEEPER_IMAGE",
		"GO_SHKEEPER_DOCKER_USER",
		"MARIADB_DATABASE_URL",
		"require_mariadb_url",
		"reject_postgres_url",
		`--user "$GO_SHKEEPER_DOCKER_USER"`,
		"ADMIN_PASSWORD_FILE",
		"ADMIN_ACCOUNT_REPORT_FILE",
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("password helper is missing %q", item)
		}
	}
	forbidden := []string{
		"#!/usr/bin/python",
		"python",
		"python3",
		"sqlite://",
		"sqlite3",
		"shkeeper.sqlite",
	}
	lowered := strings.ToLower(text)
	for _, item := range forbidden {
		if strings.Contains(lowered, item) {
			t.Fatalf("password helper must not contain %q", item)
		}
	}
}

func TestShkeeperControlScriptManagesDockerAndCryptosSafely(t *testing.T) {
	body, err := os.ReadFile("../../deploy/shkeeperctl.sh")
	if err != nil {
		t.Fatalf("read shkeeper control script: %v", err)
	}
	text := string(body)
	required := []string{
		"#!/usr/bin/env bash",
		"install_stack",
		"upgrade_stack",
		"uninstall_stack",
		"enable-crypto",
		"disable-crypto",
		"set-cryptos",
		"SHKEEPER_CRYPTOS",
		"set_wallet_envs",
		"worker_for_crypto",
		"admin-password",
		"worker-serverkey",
		"MARIADB_DATABASE_URL",
		"SHKEEPER_DRY_RUN",
		"CONFIRM_UNINSTALL=GO_SHKEEPER",
		"CONFIRM_PURGE=DELETE_GO_SHKEEPER_DATA",
		"hk-docker-debug.sh",
		"hk-16-16-final-readiness.sh",
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("shkeeper control script is missing %q", item)
		}
	}
	forbidden := []string{
		"python",
		"sqlite://",
		"sqlite3",
		"DROP DATABASE",
		`-e DATABASE_URL=`,
	}
	lowered := strings.ToLower(text)
	for _, item := range forbidden {
		if strings.Contains(lowered, strings.ToLower(item)) {
			t.Fatalf("shkeeper control script must not contain %q", item)
		}
	}

	readmeBody, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readme := string(readmeBody)
	for _, item := range []string{
		"deploy/shkeeperctl.sh",
		"enable-crypto TRX USDT BNB-USDT",
		"CONFIRM_UNINSTALL=GO_SHKEEPER",
		"SHKEEPER_COMPOSE_FILE=deploy/hk-16-16.modular.example.yml",
	} {
		if !strings.Contains(readme, item) {
			t.Fatalf("README is missing shkeeperctl marker %q", item)
		}
	}
}

func TestHKModularComposeAndDeployPlanCoverEveryDefaultCryptoWorker(t *testing.T) {
	body, err := os.ReadFile("../../deploy/hk-16-16.modular.example.yml")
	if err != nil {
		t.Fatalf("read modular compose: %v", err)
	}
	text := string(body)
	services := parseComposeServices(t, text)
	defaultCryptos := parseDefaultComposeCryptos(t, text)
	defsByName := map[string]CryptoModule{}
	for _, def := range cryptoDefinitions() {
		defsByName[def.Name] = def
	}

	requiredWorkerHosts := map[string]struct{}{}
	for crypto := range defaultCryptos {
		def, ok := defsByName[crypto]
		if !ok {
			t.Fatalf("default SHKEEPER_CRYPTOS includes %s but cryptoDefinitions does not", crypto)
		}
		if def.Adapter != "backend" {
			continue
		}
		requiredWorkerHosts[def.DefaultHost] = struct{}{}
		service, ok := services[def.DefaultHost]
		if !ok {
			t.Fatalf("default crypto %s expects worker service %s, but hk modular compose is missing it", crypto, def.DefaultHost)
		}
		if service.ContainerName == "" {
			t.Fatalf("worker service %s must have container_name for post-cutover inventory checks", def.DefaultHost)
		}
	}
	if services["shkeeper"].ContainerName != "go-shkeeper" {
		t.Fatalf("main compose service should target go-shkeeper container, got %+v", services["shkeeper"])
	}

	plan := readDeployCheckPlan(t)
	coverage := stringSet(plan.CoverageCryptos)
	for crypto := range defaultCryptos {
		if _, ok := coverage[crypto]; !ok {
			t.Fatalf("deploy-check coverage_cryptos is missing hk default crypto %s", crypto)
		}
	}
	for crypto := range coverage {
		if _, ok := defaultCryptos[crypto]; !ok {
			t.Fatalf("deploy-check coverage_cryptos includes %s but hk default SHKEEPER_CRYPTOS does not", crypto)
		}
	}
	if hostFromURL(t, plan.MainURL) != "shkeeper" {
		t.Fatalf("deploy-check main_url should use compose service shkeeper, got %s", plan.MainURL)
	}
	planWorkerHosts := map[string]struct{}{}
	for name, rawURL := range plan.WorkerURLs {
		host := hostFromURL(t, rawURL)
		if _, ok := services[host]; !ok {
			t.Fatalf("deploy-check worker %s points at %s, but hk modular compose has no such service", name, host)
		}
		planWorkerHosts[host] = struct{}{}
	}
	for host := range requiredWorkerHosts {
		if _, ok := planWorkerHosts[host]; !ok {
			t.Fatalf("deploy-check worker_urls is missing required hk worker host %s", host)
		}
	}
}

func TestDeployReadmePostCutoverInventoryChecksMatchHKCompose(t *testing.T) {
	composeBody, err := os.ReadFile("../../deploy/hk-16-16.modular.example.yml")
	if err != nil {
		t.Fatalf("read modular compose: %v", err)
	}
	readmeBody, err := os.ReadFile("../../deploy/README.md")
	if err != nil {
		t.Fatalf("read deploy README: %v", err)
	}
	services := parseComposeServices(t, string(composeBody))
	readme := string(readmeBody)
	expectedLine := readmeEnvLine(t, readme, "POST_CUTOVER_EXPECTED_CONTAINERS")
	workerLine := readmeEnvLine(t, readme, "POST_CUTOVER_WORKER_URLS")
	for serviceName, service := range services {
		if !strings.Contains(service.Image, "GO_SHKEEPER_IMAGE") {
			continue
		}
		if service.ContainerName == "" {
			t.Fatalf("go service %s has no container_name", serviceName)
		}
		want := service.ContainerName + "=go-shkeeper"
		if !strings.Contains(expectedLine, want) {
			t.Fatalf("post-cutover expected container list is missing %s", want)
		}
		if strings.HasSuffix(serviceName, "-worker") {
			wantWorkerURL := "http://" + serviceName + ":6000"
			if !strings.Contains(workerLine, wantWorkerURL) {
				t.Fatalf("post-cutover worker URL list is missing %s", wantWorkerURL)
			}
		}
	}
	forbiddenLine := readmeEnvLine(t, readme, "POST_CUTOVER_FORBIDDEN_CONTAINERS")
	for _, oldName := range []string{"shkeeper", "bnb-shkeeper", "bnb_tasks", "tron-shkeeper", "tron_tasks"} {
		if !strings.Contains(forbiddenLine, oldName) {
			t.Fatalf("post-cutover forbidden container list is missing legacy name %s", oldName)
		}
	}
}

func TestDockerfileAllowsComposeCommandOverride(t *testing.T) {
	body, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(body)
	if strings.Contains(text, "ENTRYPOINT") {
		t.Fatalf("Dockerfile must not pin ENTRYPOINT; compose worker services override CMD with /app/chain-worker")
	}
	if !strings.Contains(text, `CMD ["/app/shkeeper"]`) {
		t.Fatalf("Dockerfile should default to /app/shkeeper with CMD")
	}
}

type composeService struct {
	ContainerName string
	Image         string
}

type deployCheckPlan struct {
	MainURL         string            `json:"main_url"`
	WorkerURLs      map[string]string `json:"worker_urls"`
	CoverageCryptos []string          `json:"coverage_cryptos"`
}

func parseComposeServices(t *testing.T, text string) map[string]composeService {
	t.Helper()
	services := map[string]composeService{}
	inServices := false
	current := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "services:" {
			inServices = true
			continue
		}
		if !inServices {
			continue
		}
		if strings.HasPrefix(line, "volumes:") {
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			current = strings.TrimSuffix(strings.TrimSpace(line), ":")
			services[current] = composeService{}
			continue
		}
		if current == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "container_name:") {
			service := services[current]
			service.ContainerName = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "container_name:")))
			services[current] = service
		}
		if strings.HasPrefix(trimmed, "image:") {
			service := services[current]
			service.Image = cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "image:")))
			services[current] = service
		}
	}
	if len(services) == 0 {
		t.Fatalf("no compose services parsed")
	}
	return services
}

func cleanYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	value = strings.Trim(value, `'`)
	return value
}

func readDeployCheckPlan(t *testing.T) deployCheckPlan {
	t.Helper()
	body, err := os.ReadFile("../../deploy/deploy-check.plan.example.json")
	if err != nil {
		t.Fatalf("read deploy-check plan: %v", err)
	}
	var plan deployCheckPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("parse deploy-check plan: %v", err)
	}
	if plan.MainURL == "" || len(plan.WorkerURLs) == 0 || len(plan.CoverageCryptos) == 0 {
		t.Fatalf("deploy-check plan is missing required coverage fields: %+v", plan)
	}
	return plan
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func hostFromURL(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %s: %v", raw, err)
	}
	host := parsed.Hostname()
	if host == "" {
		t.Fatalf("url %s has no host", raw)
	}
	return host
}

func readmeEnvLine(t *testing.T, text string, key string) string {
	t.Helper()
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if strings.Contains(line, "-e "+key+"=") {
			return line
		}
	}
	t.Fatalf("deploy README is missing %s example line", key)
	return ""
}

func TestDockerfileBuildsGoRuntimeWithoutPythonOrSQLite(t *testing.T) {
	body, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(body)
	required := []string{
		"FROM golang:",
		"COPY go.mod",
		"go build",
		`CMD ["/app/shkeeper"]`,
	}
	for _, item := range required {
		if !strings.Contains(text, item) {
			t.Fatalf("Dockerfile is missing Go runtime marker %q", item)
		}
	}
	forbidden := []string{
		"python",
		"pip",
		"requirements.txt",
		"sqlite",
		"sqlite3",
	}
	lowered := strings.ToLower(text)
	for _, item := range forbidden {
		if strings.Contains(lowered, item) {
			t.Fatalf("Dockerfile must not include %q", item)
		}
	}
}

func TestProductionGoSourcesDoNotUsePanic(t *testing.T) {
	root := filepath.Clean("../..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "panic(") {
			t.Fatalf("production Go source must return errors instead of panic: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go sources: %v", err)
	}
}

func TestDockerfilesBuildAndCopyEveryRuntimeBinary(t *testing.T) {
	files := []string{
		"../../Dockerfile",
	}
	binaries := []string{
		"shkeeper",
		"chain-worker",
		"admin-account",
		"worker-serverkey",
		"deploy-check",
		"final-plan",
		"cutover-preflight",
		"import-legacy-accounts",
		"import-legacy-main-mariadb",
		"import-legacy-json",
		"cutover-audit",
		"post-cutover-verify",
		"release-audit",
		"runtime-audit",
		"goal-audit",
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read Dockerfile %s: %v", file, err)
		}
		text := string(body)
		for _, binary := range binaries {
			if !strings.Contains(text, "-o /out/"+binary) {
				t.Fatalf("%s does not build /out/%s", file, binary)
			}
			if !strings.Contains(text, "COPY --from=build /out/"+binary+" /app/"+binary) {
				t.Fatalf("%s does not copy /app/%s", file, binary)
			}
		}
	}
}
