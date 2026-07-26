package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jinleili-zz/nsp-platform/taskqueue/asynqbroker"
	_ "github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/operation"
)

func TestAZWriteRoutesExposeSagaCompatibleResponseContract(t *testing.T) {
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

	redisOpt := asynq.RedisClientOpt{Addr: redisAddr, DB: 14}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr, DB: 14})
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
	keyPrefix := "contract-" + unique
	vpcID := uuid.NewString()
	vpcName := "contract-vpc-" + unique
	subnetName := "contract-subnet-" + unique
	pccnName := "contract-pccn-" + unique
	var resourceIDs []string
	t.Cleanup(func() {
		for _, resourceID := range resourceIDs {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE resource_id = $1)`, resourceID)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM tasks WHERE resource_id = $1`, resourceID)
		}
		_, _ = db.ExecContext(context.Background(), `DELETE FROM orchestration_operations WHERE idempotency_key LIKE $1`, keyPrefix+"%")
		_, _ = db.ExecContext(context.Background(), `DELETE FROM pccn_resources WHERE pccn_name = $1`, pccnName)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM subnet_resources WHERE subnet_name = $1`, subnetName)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM vpc_resources WHERE vpc_name = $1`, vpcName)
	})

	cfg := &config.NSPConfig{Region: "region-contract", AZ: "az-contract"}
	server := NewServer(cfg, broker, nil, nil, db, nil)

	vpcBody := map[string]any{
		"vpc_id": vpcID, "vpc_name": vpcName, "region": cfg.Region,
		"vrf_name": "vrf-contract", "vlan_id": 3101, "firewall_zone": "zone-contract",
		"_root_operation_id": "top-root-" + unique, "_parent_operation_id": "top-parent-" + unique, "_resource_generation": 1,
	}
	vpc := postContractJSON(t, server, "/api/v1/vpc", keyPrefix+"-vpc", vpcBody)
	assertContractResponse(t, vpc, "vpc_id", vpcID, "workflow_id")
	operationID, _ := vpc["operation_id"].(string)
	operationRecorder := httptest.NewRecorder()
	server.router.ServeHTTP(operationRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+operationID, nil))
	if operationRecorder.Code != http.StatusOK {
		t.Fatalf("GET operation status=%d body=%s", operationRecorder.Code, operationRecorder.Body.String())
	}
	var operationResponse map[string]any
	if err := json.Unmarshal(operationRecorder.Body.Bytes(), &operationResponse); err != nil {
		t.Fatalf("decode operation response: %v", err)
	}
	if operationResponse["operation_id"] != operationID || operationResponse["resource_id"] != vpcID || operationResponse["generation"] != float64(1) {
		t.Fatalf("operation response identity invalid: %#v", operationResponse)
	}
	if operationResponse["root_operation_id"] != "top-root-"+unique || operationResponse["parent_operation_id"] != "top-parent-"+unique {
		t.Fatalf("Saga payload ancestry was not preserved: %#v", operationResponse)
	}
	if _, exposed := operationResponse["idempotency_key"]; exposed {
		t.Fatalf("operation response exposed raw idempotency key: %#v", operationResponse)
	}
	vpcReplay := postContractJSON(t, server, "/api/v1/vpc", keyPrefix+"-vpc", vpcBody)
	if vpcReplay["operation_id"] != vpc["operation_id"] || vpcReplay["resource_id"] != vpc["resource_id"] {
		t.Fatalf("VPC replay identity changed: first=%#v replay=%#v", vpc, vpcReplay)
	}
	conflictingVPCBody := map[string]any{
		"vpc_id": vpcID, "vpc_name": vpcName, "region": cfg.Region,
		"vrf_name": "vrf-conflict", "vlan_id": 3101, "firewall_zone": "zone-contract",
	}
	conflictRecorder := postContractRequest(t, server, "/api/v1/vpc", keyPrefix+"-vpc", conflictingVPCBody)
	if conflictRecorder.Code != http.StatusConflict {
		t.Fatalf("same key with different body status=%d, want 409; body=%s", conflictRecorder.Code, conflictRecorder.Body.String())
	}
	var conflictResponse map[string]any
	if err := json.Unmarshal(conflictRecorder.Body.Bytes(), &conflictResponse); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if conflictResponse["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("conflict code=%#v, want IDEMPOTENCY_KEY_REUSED", conflictResponse["code"])
	}
	resourceIDs = append(resourceIDs, vpcID)

	subnet := postContractJSON(t, server, "/api/v1/subnet", keyPrefix+"-subnet", map[string]any{
		"subnet_name": subnetName, "vpc_name": vpcName, "region": cfg.Region,
		"az": cfg.AZ, "cidr": "10.231.0.0/24",
	})
	subnetID, _ := subnet["subnet_id"].(string)
	assertContractResponse(t, subnet, "subnet_id", subnetID, "workflow_id")
	resourceIDs = append(resourceIDs, subnetID)

	pccn := postContractJSON(t, server, "/api/v1/pccn", keyPrefix+"-pccn", map[string]any{
		"pccn_name": pccnName,
		"vpc1":      map[string]any{"vpc_name": vpcName, "region": cfg.Region},
		"vpc2":      map[string]any{"vpc_name": "peer-" + unique, "region": "peer-region"},
	})
	pccnID, _ := pccn["pccn_id"].(string)
	assertContractResponse(t, pccn, "pccn_id", pccnID, "tx_id")
	resourceIDs = append(resourceIDs, pccnID)
	extraSubnetID := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO subnet_resources (id, subnet_name, vpc_name, region, az, cidr, status, total_tasks, completed_tasks, failed_tasks)
		VALUES ($1, $2, $3, $4, $5, '10.232.0.0/24', 'running', 0, 0, 0)
	`, extraSubnetID, "contract-later-subnet-"+unique, vpcName, cfg.Region, cfg.AZ); err != nil {
		t.Fatalf("insert later subnet: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM subnet_resources WHERE id = $1`, extraSubnetID) })

	retrySuppliedID := uuid.NewString()
	pccnRetry := postContractJSON(t, server, "/api/v1/pccn", keyPrefix+"-pccn", map[string]any{
		"pccn_id": retrySuppliedID, "pccn_name": pccnName,
		"vpc1": map[string]any{"vpc_name": vpcName, "region": cfg.Region},
		"vpc2": map[string]any{"vpc_name": "peer-" + unique, "region": "peer-region"},
	})
	assertContractResponse(t, pccnRetry, "pccn_id", pccnID, "tx_id")
	var orphanTasks int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE resource_id = $1`, retrySuppliedID).Scan(&orphanTasks); err != nil {
		t.Fatalf("count orphan tasks: %v", err)
	}
	if orphanTasks != 0 {
		t.Fatalf("tasks linked to unpersisted retry resource ID = %d, want 0", orphanTasks)
	}

	parallelVPCID := uuid.NewString()
	parallelVPCName := "contract-parallel-vpc-" + unique
	parallelBody := map[string]any{
		"vpc_id": parallelVPCID, "vpc_name": parallelVPCName, "region": cfg.Region,
		"vrf_name": "vrf-parallel", "vlan_id": 3102, "firewall_zone": "zone-parallel",
	}
	resourceIDs = append(resourceIDs, parallelVPCID)
	const concurrentRequests = 100
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, concurrentRequests)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(concurrentRequests)
	done.Add(concurrentRequests)
	for i := 0; i < concurrentRequests; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			results <- postContractRequest(t, server, "/api/v1/vpc", keyPrefix+"-parallel-vpc", parallelBody)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(results)
	var parallelOperationID string
	for recorder := range results {
		if recorder.Code != http.StatusOK {
			t.Fatalf("parallel POST status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode parallel response: %v", err)
		}
		if parallelOperationID == "" {
			parallelOperationID, _ = response["operation_id"].(string)
		}
		if response["operation_id"] != parallelOperationID || response["resource_id"] != parallelVPCID {
			t.Fatalf("parallel response identity changed: %#v", response)
		}
	}
	var operationCount, resourceCount, taskCount, outboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_operations WHERE idempotency_key = $1`, keyPrefix+"-parallel-vpc").Scan(&operationCount); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vpc_resources WHERE id = $1`, parallelVPCID).Scan(&resourceCount); err != nil {
		t.Fatalf("count resources: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE resource_id = $1`, parallelVPCID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE resource_id = $1)`, parallelVPCID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if operationCount != 1 || resourceCount != 1 || taskCount != 3 || outboxCount != 1 {
		t.Fatalf("parallel operation/resource/task/outbox=%d/%d/%d/%d, want 1/1/3/1", operationCount, resourceCount, taskCount, outboxCount)
	}
}

func postContractJSON(t *testing.T, server *Server, path, idempotencyKey string, body any) map[string]any {
	t.Helper()
	recorder := postContractRequest(t, server, path, idempotencyKey, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200; body=%s", path, recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode POST %s response: %v; body=%s", path, err, recorder.Body.String())
	}
	return response
}

func postContractRequest(t *testing.T, server *Server, path, idempotencyKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(operation.HeaderSagaTransactionID, "saga-contract")
	req.Header.Set(operation.HeaderIdempotencyKey, idempotencyKey)
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, req)
	return recorder
}

func assertContractResponse(t *testing.T, response map[string]any, legacyIDField, wantID, workflowField string) {
	t.Helper()
	if response["code"] != "0" || response["success"] != true {
		t.Fatalf("response code/success = %#v/%#v, want 0/true; response=%#v", response["code"], response["success"], response)
	}
	if wantID == "" || response[legacyIDField] != wantID {
		t.Fatalf("legacy %s = %#v, want %q", legacyIDField, response[legacyIDField], wantID)
	}
	if response["resource_id"] != wantID {
		t.Fatalf("resource_id = %#v, want %q", response["resource_id"], wantID)
	}
	if response["status"] != "accepted" {
		t.Fatalf("status = %#v, want accepted", response["status"])
	}
	if response[workflowField] == "" {
		t.Fatalf("legacy workflow field %s missing; response=%#v", workflowField, response)
	}
}
