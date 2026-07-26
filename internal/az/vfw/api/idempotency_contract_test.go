package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jinleili-zz/nsp-platform/taskqueue/asynqbroker"
	_ "github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/operation"
)

func TestAZVFWWriteRouteExposesSagaCompatibleResponseContract(t *testing.T) {
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("NSP_TEST_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN and NSP_TEST_REDIS_ADDR are required")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	redisOpt := asynq.RedisClientOpt{Addr: redisAddr, DB: 13}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr, DB: 13})
	if err := redisClient.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush Redis test DB: %v", err)
	}
	t.Cleanup(func() {
		_ = redisClient.FlushDB(context.Background()).Err()
		_ = redisClient.Close()
	})
	broker := asynqbroker.NewBroker(redisOpt)
	t.Cleanup(func() { _ = broker.Close() })

	unique := uuid.NewString()
	policyName := "contract-policy-" + unique
	idempotencyKey := "contract-policy-key-" + unique
	var policyID string
	t.Cleanup(func() {
		if policyID != "" {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE resource_id = $1)`, policyID)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM tasks WHERE resource_id = $1`, policyID)
		}
		_, _ = db.ExecContext(context.Background(), `DELETE FROM orchestration_operations WHERE idempotency_key = $1`, idempotencyKey)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM firewall_policies WHERE policy_name = $1`, policyName)
	})

	cfg := &config.NSPConfig{Region: "region-contract", AZ: "az-contract"}
	server := NewServer(cfg, broker, nil, nil, db, nil)
	body, err := json.Marshal(map[string]any{
		"policy_name": policyName, "source_zone": "zone-a", "dest_zone": "zone-b",
		"source_ip": "10.0.0.1", "dest_ip": "10.1.0.1", "source_port": "443",
		"dest_port": "8443", "protocol": "tcp", "action": "allow",
		"region": cfg.Region, "az": cfg.AZ,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/firewall/policy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(operation.HeaderSagaTransactionID, "saga-vfw-contract")
	req.Header.Set(operation.HeaderIdempotencyKey, idempotencyKey)
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	policyID, _ = response["policy_id"].(string)
	if response["code"] != "0" || response["success"] != true {
		t.Fatalf("response code/success = %#v/%#v, want 0/true; response=%#v", response["code"], response["success"], response)
	}
	if policyID == "" || response["workflow_id"] == "" {
		t.Fatalf("legacy fields missing; response=%#v", response)
	}
	if response["resource_id"] != policyID || response["status"] != "accepted" {
		t.Fatalf("common resource/status fields invalid; response=%#v", response)
	}
	replayReq := httptest.NewRequest(http.MethodPost, "/api/v1/firewall/policy", bytes.NewReader(body))
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set(operation.HeaderSagaTransactionID, "saga-vfw-contract")
	replayReq.Header.Set(operation.HeaderIdempotencyKey, idempotencyKey)
	replayRecorder := httptest.NewRecorder()
	server.router.ServeHTTP(replayRecorder, replayReq)
	if replayRecorder.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayRecorder.Code, replayRecorder.Body.String())
	}
	var replayResponse map[string]any
	if err := json.Unmarshal(replayRecorder.Body.Bytes(), &replayResponse); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replayResponse["operation_id"] != response["operation_id"] || replayResponse["resource_id"] != policyID {
		t.Fatalf("replay identity changed: first=%#v replay=%#v", response, replayResponse)
	}
	var conflictingBody map[string]any
	if err := json.Unmarshal(body, &conflictingBody); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	conflictingBody["action"] = "deny"
	conflictingJSON, _ := json.Marshal(conflictingBody)
	conflictReq := httptest.NewRequest(http.MethodPost, "/api/v1/firewall/policy", bytes.NewReader(conflictingJSON))
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictReq.Header.Set(operation.HeaderSagaTransactionID, "saga-vfw-contract")
	conflictReq.Header.Set(operation.HeaderIdempotencyKey, idempotencyKey)
	conflictRecorder := httptest.NewRecorder()
	server.router.ServeHTTP(conflictRecorder, conflictReq)
	if conflictRecorder.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d, want 409; body=%s", conflictRecorder.Code, conflictRecorder.Body.String())
	}
}
