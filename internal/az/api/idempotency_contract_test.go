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
	vpcID := uuid.NewString()
	vpcName := "contract-vpc-" + unique
	subnetName := "contract-subnet-" + unique
	pccnName := "contract-pccn-" + unique
	var resourceIDs []string
	t.Cleanup(func() {
		for _, resourceID := range resourceIDs {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM tasks WHERE resource_id = $1`, resourceID)
		}
		_, _ = db.ExecContext(context.Background(), `DELETE FROM pccn_resources WHERE pccn_name = $1`, pccnName)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM subnet_resources WHERE subnet_name = $1`, subnetName)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM vpc_resources WHERE vpc_name = $1`, vpcName)
	})

	cfg := &config.NSPConfig{Region: "region-contract", AZ: "az-contract"}
	server := NewServer(cfg, broker, nil, nil, db, nil)

	vpc := postContractJSON(t, server, "/api/v1/vpc", map[string]any{
		"vpc_id": vpcID, "vpc_name": vpcName, "region": cfg.Region,
		"vrf_name": "vrf-contract", "vlan_id": 3101, "firewall_zone": "zone-contract",
	})
	assertContractResponse(t, vpc, "vpc_id", vpcID, "workflow_id")
	resourceIDs = append(resourceIDs, vpcID)

	subnet := postContractJSON(t, server, "/api/v1/subnet", map[string]any{
		"subnet_name": subnetName, "vpc_name": vpcName, "region": cfg.Region,
		"az": cfg.AZ, "cidr": "10.231.0.0/24",
	})
	subnetID, _ := subnet["subnet_id"].(string)
	assertContractResponse(t, subnet, "subnet_id", subnetID, "workflow_id")
	resourceIDs = append(resourceIDs, subnetID)

	pccn := postContractJSON(t, server, "/api/v1/pccn", map[string]any{
		"pccn_name": pccnName,
		"vpc1":      map[string]any{"vpc_name": vpcName, "region": cfg.Region},
		"vpc2":      map[string]any{"vpc_name": "peer-" + unique, "region": "peer-region"},
	})
	pccnID, _ := pccn["pccn_id"].(string)
	assertContractResponse(t, pccn, "pccn_id", pccnID, "tx_id")
	resourceIDs = append(resourceIDs, pccnID)

	retrySuppliedID := uuid.NewString()
	pccnRetry := postContractJSON(t, server, "/api/v1/pccn", map[string]any{
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
}

func postContractJSON(t *testing.T, server *Server, path string, body any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(operation.HeaderSagaTransactionID, "saga-contract")
	req.Header.Set(operation.HeaderIdempotencyKey, "step-contract-"+path)
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200; body=%s", path, recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode POST %s response: %v; body=%s", path, err, recorder.Body.String())
	}
	return response
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
