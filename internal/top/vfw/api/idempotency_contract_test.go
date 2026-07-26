package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"workflow_qoder/internal/operation"
	"workflow_qoder/internal/top/vfw/service"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestTopVFWCreateReplaysOperationAndRejectsChangedBody(t *testing.T) {
	db := openTopVFWContractDB(t)
	policyService := service.NewPolicyService(db, db, nil)
	server := NewServer(policyService)
	key := "top-vfw-contract-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE operation_id IN (SELECT operation_id FROM orchestration_operations WHERE owner_service = 'top-nsp-vfw' AND idempotency_key = $1)`, key)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vfw' AND idempotency_key = $1`, key)
	})
	body := map[string]any{"policy_name": "policy-contract", "source_ip": "198.51.100.1", "dest_ip": "203.0.113.1", "source_port": "443", "dest_port": "8443", "protocol": "tcp", "action": "allow"}
	firstStatus, first := postTopPolicy(t, server.router, key, body)
	secondStatus, second := postTopPolicy(t, server.router, key, body)
	if firstStatus != http.StatusBadRequest || secondStatus != http.StatusBadRequest {
		t.Fatalf("first/second status = %d/%d", firstStatus, secondStatus)
	}
	if first["operation_id"] == "" || first["operation_id"] != second["operation_id"] || first["resource_id"] != second["resource_id"] {
		t.Fatalf("first/second = %#v / %#v", first, second)
	}
	body["action"] = "deny"
	status, conflict := postTopPolicy(t, server.router, key, body)
	if status != http.StatusConflict || conflict["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("conflict status/body = %d/%#v", status, conflict)
	}
	activeName := "policy-active-" + uuid.NewString()
	activeBody := map[string]any{"policy_name": activeName, "source_ip": "198.51.100.1", "dest_ip": "203.0.113.1", "source_port": "443", "dest_port": "8443", "protocol": "tcp", "action": "allow"}
	seedActiveVFWOperation(t, db, key+"-active-seed", activeName, activeBody)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vfw' AND target_scope = $1`, activeName)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vfw' AND target_scope = $1`, activeName)
	})
	activeBody["action"] = "deny"
	resourceKey := key + "-resource-conflict"
	status, resourceConflict := postTopPolicy(t, server.router, resourceKey, activeBody)
	if status != http.StatusConflict || resourceConflict["code"] != "RESOURCE_SPEC_CONFLICT" {
		t.Fatalf("resource conflict status/body = %d/%#v", status, resourceConflict)
	}
	status, resourceConflict = postTopPolicy(t, server.router, resourceKey, activeBody)
	if status != http.StatusConflict || resourceConflict["code"] != "RESOURCE_SPEC_CONFLICT" {
		t.Fatalf("replayed resource conflict status/body = %d/%#v", status, resourceConflict)
	}
}

func seedActiveVFWOperation(t *testing.T, db *sql.DB, key, target string, payload any) {
	t.Helper()
	opService := operation.NewService(operation.NewRepository(db))
	op, decision, err := opService.BeginTarget(t.Context(), operation.BeginRequest{
		OwnerService: "top-nsp-vfw", CallerScope: "northbound:compat", RouteScope: "POST /api/v1/firewall/policy",
		OperationType: "apply_firewall_policy", TargetScope: target, IdempotencyKey: key, Payload: payload,
		ResourceType: "firewall_policy", ResourceID: uuid.NewString(), Generation: 1,
	})
	if err != nil || decision != operation.DecisionNew {
		t.Fatalf("seed VFW operation: op=%#v decision=%s err=%v", op, decision, err)
	}
	lease, acquired, err := opService.ClaimDispatch(t.Context(), op.OperationID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("claim VFW seed: acquired=%v err=%v", acquired, err)
	}
	if stored, err := opService.StoreResponse(lease.Context(t.Context()), op.OperationID, operation.StatusRunning, "0", map[string]any{"code": "0", "success": true, "operation_id": op.OperationID, "resource_id": op.ResourceID, "status": "running"}); err != nil || !stored {
		t.Fatalf("store VFW seed: stored=%v err=%v", stored, err)
	}
	lease.Close()
}

func postTopPolicy(t *testing.T, handler http.Handler, key string, body map[string]any) (int, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/firewall/policy", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %s: %v", recorder.Body.String(), err)
	}
	return recorder.Code, response
}

func openTopVFWContractDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	return db
}
