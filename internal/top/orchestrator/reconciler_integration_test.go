package orchestrator

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/saga"
	_ "github.com/lib/pq"
)

func TestFinishVPCOperationConvergesRunningToSucceeded(t *testing.T) {
	db := openTopReconcilerDB(t)
	service := operation.NewService(operation.NewRepository(db))
	owner := "top-finish-test-" + uuid.NewString()
	t.Cleanup(func() { cleanupTopReconcilerOperations(t, db, owner) })
	op, decision, err := service.BeginTarget(context.Background(), operation.BeginRequest{
		OwnerService: owner, CallerScope: "test", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc",
		TargetScope: "vpc-a", IdempotencyKey: "finish-key", Payload: models.VPCRequest{VPCName: "vpc-a", Region: "region-a"},
		ResourceType: "vpc", ResourceID: uuid.NewString(),
	})
	if err != nil || decision != operation.DecisionNew {
		t.Fatalf("begin operation: %#v/%s/%v", op, decision, err)
	}
	lease, acquired, err := service.ClaimDispatch(context.Background(), op.OperationID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire dispatch: %v/%v", acquired, err)
	}
	if stored, err := service.StoreResponse(lease.Context(context.Background()), op.OperationID, operation.StatusRunning, "0", models.VPCResponse{Success: true, OperationID: op.OperationID, Status: "running"}); err != nil || !stored {
		t.Fatalf("store running response: %v/%v", stored, err)
	}
	lease.Close()

	orch := &Orchestrator{operationService: service}
	orch.finishVPCOperation(op.OperationID, "saga-1", "vpc-a", operation.StatusSucceeded, "done")
	loaded, err := service.Get(context.Background(), op.OperationID)
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if loaded.Status != operation.StatusSucceeded || loaded.ResponseCode != "0" || loaded.CompletedAt == nil {
		t.Fatalf("finished operation = %#v", loaded)
	}
}

func TestUniqueSortedAZsIsStableAcrossMapDerivedOrder(t *testing.T) {
	input := []*models.AZ{
		{Region: "region-b", ID: "az-a"},
		{Region: "region-a", ID: "az-b"},
		{Region: "region-a", ID: "az-a"},
		{Region: "region-a", ID: "az-b"},
	}
	got := uniqueSortedAZs(input)
	if len(got) != 3 || got[0].Region != "region-a" || got[0].ID != "az-a" || got[1].ID != "az-b" || got[2].Region != "region-b" {
		t.Fatalf("sorted AZs = %#v", got)
	}
}

func TestSagaDefinitionSnapshotsRestoreOriginalAZSet(t *testing.T) {
	vpcDefinition := &saga.SagaDefinition{Steps: []saga.Step{
		{Name: "创建VPC-az-b", ActionURL: "http://old-b/api/v1/vpc"},
		{Name: "创建VPC-az-a", ActionURL: "http://old-a/api/v1/vpc"},
	}}
	vpcAZs := vpcAZsFromDefinition("region-a", vpcDefinition)
	if len(vpcAZs) != 2 || vpcAZs[0].ID != "az-a" || vpcAZs[0].NSPAddr != "http://old-a" || vpcAZs[1].ID != "az-b" {
		t.Fatalf("restored VPC AZs = %#v", vpcAZs)
	}
	vpc1 := &models.VPCRegistry{Region: "region-a", AZDetails: map[string]models.AZDetail{"az-a": {}}}
	vpc2 := &models.VPCRegistry{Region: "region-b", AZDetails: map[string]models.AZDetail{"az-b": {}}}
	pccnDefinition := &saga.SagaDefinition{Steps: []saga.Step{
		{Name: "提交PCCN创建-az-b", ActionURL: "http://old-b/api/v1/pccn"},
		{Name: "提交PCCN创建-az-a", ActionURL: "http://old-a/api/v1/pccn"},
	}}
	pccnAZs := pccnAZsFromDefinition(pccnDefinition, vpc1, vpc2)
	if len(pccnAZs) != 2 || pccnAZs[0].Region != "region-a" || pccnAZs[0].NSPAddr != "http://old-a" || pccnAZs[1].Region != "region-b" {
		t.Fatalf("restored PCCN AZs = %#v", pccnAZs)
	}
}

func openTopReconcilerDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	migration, err := os.ReadFile("../../db/migrations/005_create_operations.sql")
	if err != nil {
		t.Fatalf("read operation migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply operation migration: %v", err)
	}
	return db
}

func cleanupTopReconcilerOperations(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = $1`, owner)
	_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = $1`, owner)
}
