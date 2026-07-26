package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"workflow_qoder/internal/operation"
	"workflow_qoder/internal/top/orchestrator"
	"workflow_qoder/internal/top/registry"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

func TestTopVPCCreateReplaysOperationAndRejectsChangedBody(t *testing.T) {
	db, redisClient := openTopContractDependencies(t)
	orch := orchestrator.NewOrchestrator(t.Context(), registry.NewRegistry(redisClient), db, nil, nil, nil)
	server := NewServer(registry.NewRegistry(redisClient), orch, nil, nil)
	server.SetupRoutes()

	key := "top-vpc-contract-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vpc' AND target_scope = 'vpc-contract'`)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND idempotency_key LIKE $1`, key+"%")
	})
	body := map[string]any{"vpc_name": "vpc-contract", "region": "region-without-az", "vrf_name": "vrf-contract", "vlan_id": 101, "firewall_zone": "zone-contract"}
	firstStatus, first := postTopVPC(t, server.Engine(), key, body)
	secondStatus, second := postTopVPC(t, server.Engine(), key, body)
	if firstStatus != http.StatusBadRequest || secondStatus != http.StatusBadRequest {
		t.Fatalf("first/second status = %d/%d", firstStatus, secondStatus)
	}
	if first["operation_id"] == "" || first["operation_id"] != second["operation_id"] || first["resource_id"] != second["resource_id"] {
		t.Fatalf("first/second responses = %#v / %#v", first, second)
	}

	changed := map[string]any{"vpc_name": "vpc-contract", "region": "region-without-az", "vrf_name": "vrf-contract", "vlan_id": 202, "firewall_zone": "zone-contract"}
	status, conflict := postTopVPC(t, server.Engine(), key, changed)
	if status != http.StatusConflict || conflict["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("conflict status/body = %d/%#v", status, conflict)
	}
	activeName := "vpc-active-" + uuid.NewString()
	activeTarget := activeName
	activeBody := map[string]any{"vpc_name": activeName, "region": "region-without-az", "vrf_name": "vrf-active", "vlan_id": 101, "firewall_zone": "zone-active"}
	seedActiveTopOperation(t, db, "top-nsp-vpc", "POST /api/v1/vpc", "create_vpc", activeTarget, "vpc", key+"-seed", activeBody)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, activeTarget)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, activeTarget)
	})
	activeBody["vlan_id"] = 202
	status, resourceConflict := postTopVPC(t, server.Engine(), key+"-resource", activeBody)
	if status != http.StatusConflict || resourceConflict["code"] != "RESOURCE_SPEC_CONFLICT" {
		t.Fatalf("resource conflict status/body = %d/%#v", status, resourceConflict)
	}
	status, resourceConflictReplay := postTopVPC(t, server.Engine(), key+"-resource", activeBody)
	if status != http.StatusConflict || resourceConflictReplay["code"] != "RESOURCE_SPEC_CONFLICT" {
		t.Fatalf("replayed resource conflict status/body = %d/%#v", status, resourceConflictReplay)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+first["operation_id"].(string), nil)
	recorder := httptest.NewRecorder()
	server.Engine().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || bytes.Contains(recorder.Body.Bytes(), []byte(key)) || bytes.Contains(recorder.Body.Bytes(), []byte("request_hash")) {
		t.Fatalf("operation query status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestTopVPCCreateConcurrentRequestsHaveOneOperation(t *testing.T) {
	db, redisClient := openTopContractDependencies(t)
	reg := registry.NewRegistry(redisClient)
	orch := orchestrator.NewOrchestrator(t.Context(), reg, db, nil, nil, nil)
	server := NewServer(reg, orch, nil, nil)
	server.SetupRoutes()
	key := "top-vpc-concurrent-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE operation_id IN (SELECT operation_id FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND idempotency_key = $1)`, key)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND idempotency_key = $1`, key)
	})
	body := map[string]any{"vpc_name": "vpc-concurrent", "region": "region-without-az", "vrf_name": "vrf-concurrent", "vlan_id": 101, "firewall_zone": "zone-concurrent"}

	const contenders = 100
	start := make(chan struct{})
	results := make(chan map[string]any, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, response := postTopVPC(t, server.Engine(), key, body)
			results <- response
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var operationID, resourceID any
	for response := range results {
		if operationID == nil {
			operationID, resourceID = response["operation_id"], response["resource_id"]
		}
		if response["operation_id"] != operationID || response["resource_id"] != resourceID {
			t.Fatalf("response identity = %#v, want operation/resource %v/%v", response, operationID, resourceID)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND idempotency_key = $1`, key).Scan(&count); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if count != 1 {
		t.Fatalf("operation rows = %d, want 1", count)
	}
}

func TestTopVPCCreateDifferentKeysShareTargetOperation(t *testing.T) {
	db, redisClient := openTopContractDependencies(t)
	reg := registry.NewRegistry(redisClient)
	orch := orchestrator.NewOrchestrator(t.Context(), reg, db, nil, nil, nil)
	server := NewServer(reg, orch, nil, nil)
	server.SetupRoutes()
	unique := uuid.NewString()
	keyPrefix := "top-vpc-target-" + unique + "-"
	targetScope := "vpc-target-" + unique
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, targetScope)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND idempotency_key LIKE $1`, keyPrefix+"%")
	})
	body := map[string]any{"vpc_name": "vpc-target-" + unique, "region": "region-without-az", "vrf_name": "vrf-target", "vlan_id": 101, "firewall_zone": "zone-target"}
	seedActiveTopOperation(t, db, "top-nsp-vpc", "POST /api/v1/vpc", "create_vpc", targetScope, "vpc", keyPrefix+"0", body)

	const contenders = 100
	start := make(chan struct{})
	results := make(chan map[string]any, contenders)
	var wg sync.WaitGroup
	for index := range contenders {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, response := postTopVPC(t, server.Engine(), fmt.Sprintf("%s%d", keyPrefix, index), body)
			results <- response
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)
	var operationID, resourceID any
	for response := range results {
		if operationID == nil {
			operationID, resourceID = response["operation_id"], response["resource_id"]
		}
		if response["operation_id"] != operationID || response["resource_id"] != resourceID {
			t.Fatalf("target response identity = %#v, want %v/%v", response, operationID, resourceID)
		}
	}
	var operations, aliases int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, targetScope).Scan(&operations); err != nil {
		t.Fatalf("count target operations: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_idempotency_aliases WHERE owner_service = 'top-nsp-vpc' AND idempotency_key LIKE $1`, keyPrefix+"%").Scan(&aliases); err != nil {
		t.Fatalf("count target aliases: %v", err)
	}
	if operations != 1 || aliases != contenders-1 {
		t.Fatalf("target operations/aliases = %d/%d, want 1/%d", operations, aliases, contenders-1)
	}
}

func seedActiveTopOperation(t *testing.T, db *sql.DB, owner, route, operationType, target, resourceType, key string, payload any) *operation.Operation {
	t.Helper()
	service := operation.NewService(operation.NewRepository(db))
	op, decision, err := service.BeginTarget(context.Background(), operation.BeginRequest{
		OwnerService: owner, CallerScope: "northbound:compat", RouteScope: route, OperationType: operationType,
		TargetScope: target, IdempotencyKey: key, Payload: payload, ResourceType: resourceType, ResourceID: uuid.NewString(), Generation: 1,
	})
	if err != nil || decision != operation.DecisionNew {
		t.Fatalf("seed active operation: op=%#v decision=%s err=%v", op, decision, err)
	}
	lease, acquired, err := service.ClaimDispatch(context.Background(), op.OperationID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("claim seed operation: acquired=%v err=%v", acquired, err)
	}
	if stored, err := service.StoreResponse(lease.Context(context.Background()), op.OperationID, operation.StatusRunning, "0", map[string]any{"code": "0", "success": true, "operation_id": op.OperationID, "resource_id": op.ResourceID, "status": "running"}); err != nil || !stored {
		t.Fatalf("store seed operation: stored=%v err=%v", stored, err)
	}
	lease.Close()
	return op
}

func postTopVPC(t *testing.T, handler http.Handler, key string, body map[string]any) (int, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vpc", bytes.NewReader(payload))
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

func openTopContractDependencies(t *testing.T) (*sql.DB, *redis.Client) {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("NSP_TEST_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN and NSP_TEST_REDIS_ADDR are required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping redis: %v", err)
	}
	return db, redisClient
}
