package chainworker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("GO_SHKEEPER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GO_SHKEEPER_TEST_DATABASE_URL=mariadb://user:<password>@host:3306/testdb to run MariaDB integration tests")
	}
	cfg := Config{
		Module:        "BNB",
		DatabaseURL:   databaseURL,
		DatabaseDSN:   parseDatabaseURL(databaseURL),
		DatabaseError: databaseURLConfigError(databaseURL),
		LogLevel:      slog.LevelError,
	}
	store, err := OpenStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{"chain_task", "chain_account"} {
		if _, err := store.db.Exec("DELETE FROM " + table); err != nil {
			t.Fatalf("cleanup %s: %v", table, err)
		}
	}
	return store
}

func TestOpenStoreAppliesDatabasePoolSettings(t *testing.T) {
	databaseURL := os.Getenv("GO_SHKEEPER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GO_SHKEEPER_TEST_DATABASE_URL=mariadb://user:<password>@host:3306/testdb to run MariaDB integration tests")
	}
	cfg := Config{
		Module:            "BNB",
		DatabaseURL:       databaseURL,
		DatabaseDSN:       parseDatabaseURL(databaseURL),
		DatabaseError:     databaseURLConfigError(databaseURL),
		DBMaxOpenConns:    9,
		DBMaxIdleConns:    4,
		DBConnMaxIdleTime: 13 * time.Second,
		DBConnMaxLifetime: 23 * time.Second,
		LogLevel:          slog.LevelError,
	}
	store, err := OpenStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 9 {
		t.Fatalf("max open connections=%d, want 9", got)
	}
}

func TestHealthAndReadyEndpoints(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	cfg := Config{Module: "BNB", Username: "worker", Password: "secret", RequestTimeout: 5}
	handler := NewServer(cfg, store, slog.New(slog.NewTextHandler(os.Stdout, nil))).Routes()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz status=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"ready"`) {
		t.Fatalf("readyz status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestStoreAccountAndTask(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	account := Account{Module: "BNB", Crypto: "BNB", Address: "0x0000000000000000000000000000000000000001", PrivateKeyHex: "v1:test"}
	if err := store.AddAccount(ctx, &account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	accounts, err := store.ListAccounts(ctx, "BNB", "BNB")
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Address != account.Address {
		t.Fatalf("unexpected accounts: %+v", accounts)
	}

	req, _ := json.Marshal(map[string]string{"amount": "1"})
	res, _ := json.Marshal(map[string]string{"status": "PENDING"})
	task := Task{ID: "test-task", Module: "BNB", Crypto: "BNB", Kind: "payout", Status: "PENDING", Request: req, Result: res}
	if err := store.AddTask(ctx, task); err != nil {
		t.Fatalf("add task: %v", err)
	}
	loaded, err := store.Task(ctx, "test-task")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if loaded.Status != "PENDING" || loaded.Kind != "payout" {
		t.Fatalf("unexpected task: %+v", loaded)
	}
	updatedResult, _ := json.Marshal(map[string]any{"txids": []string{"0xabc"}})
	if err := store.UpdateTask(ctx, "test-task", "SUCCESS", updatedResult); err != nil {
		t.Fatalf("update task: %v", err)
	}
	loaded, err = store.Task(ctx, "test-task")
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if loaded.Status != "SUCCESS" {
		t.Fatalf("unexpected updated task: %+v", loaded)
	}
}

func TestDumpAccountsEndpointReturnsEncryptedKeys(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	account := Account{Module: "BNB", Crypto: "BNB", Address: "0x0000000000000000000000000000000000000002", PrivateKeyHex: "v1:encrypted"}
	if err := store.AddAccount(ctx, &account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	cfg := Config{Module: "BNB", Username: "worker", Password: "secret", RequestTimeout: 5}
	handler := NewServer(cfg, store, slog.New(slog.NewTextHandler(os.Stdout, nil))).Routes()
	req := httptest.NewRequest(http.MethodGet, "/BNB/dump", nil)
	req.SetBasicAuth("worker", "secret")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"private_key_encrypted":"v1:encrypted"`) {
		t.Fatalf("dump status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestBitcoinLikeWorkerPayoutEndpointPersistsTask(t *testing.T) {
	store := testStore(t)
	defer store.Close()

	var sawSetFee bool
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		var result any
		switch req.Method {
		case "settxfee":
			sawSetFee = true
			result = true
		case "sendtoaddress":
			if len(req.Params) < 2 || req.Params[0] != "ltc-dest" || req.Params[1] != "1.25" {
				t.Fatalf("unexpected sendtoaddress params: %+v", req.Params)
			}
			result = "ltc-payout-tx"
		default:
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": "test"})
	}))
	defer rpc.Close()

	cfg := Config{Module: "LTC", FullnodeURL: rpc.URL, Username: "worker", Password: "secret", RequestTimeout: time.Second}
	handler := NewServer(cfg, store, slog.New(slog.NewTextHandler(os.Stdout, nil))).Routes()
	req := httptest.NewRequest(http.MethodPost, "/LTC/payout/ltc-dest/1.25/2", nil)
	req.SetBasicAuth("worker", "secret")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("payout status=%d body=%s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode payout response: %v", err)
	}
	taskID, _ := body["task_id"].(string)
	if taskID == "" || body["status"] != "SUCCESS" || !sawSetFee {
		t.Fatalf("unexpected payout response: %+v sawSetFee=%v", body, sawSetFee)
	}
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("payout response result is missing: %+v", body)
	}
	txids, ok := result["txids"].([]any)
	if !ok || len(txids) != 1 || txids[0] != "ltc-payout-tx" || result["dest"] != "ltc-dest" {
		t.Fatalf("unexpected payout result: %+v", result)
	}
	task, err := store.Task(context.Background(), taskID)
	if err != nil {
		t.Fatalf("load payout task: %v", err)
	}
	if task.Module != "LTC" || task.Crypto != "LTC" || task.Kind != "payout" || task.Status != "SUCCESS" {
		t.Fatalf("unexpected stored task: %+v", task)
	}
}
