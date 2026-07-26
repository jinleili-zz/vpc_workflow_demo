package dao

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"workflow_qoder/internal/models"
)

func TestPCCNCreateReusesPersistedResourceID(t *testing.T) {
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	ctx := context.Background()
	name := "pccn-idempotency-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM pccn_resources WHERE pccn_name = $1`, name)
	})

	dao := NewPCCNDAO(db)
	first := newPCCNForCreate(uuid.NewString(), name)
	persistedFirst, err := dao.Create(ctx, first)
	if err != nil {
		t.Fatalf("create first PCCN: %v", err)
	}

	second := newPCCNForCreate(uuid.NewString(), name)
	if second.ID == first.ID {
		t.Fatal("test setup generated identical IDs")
	}
	persistedSecond, err := dao.Create(ctx, second)
	if err != nil {
		t.Fatalf("repeat PCCN create: %v", err)
	}

	if persistedFirst.ID != first.ID {
		t.Fatalf("first persisted ID = %q, want %q", persistedFirst.ID, first.ID)
	}
	if persistedSecond.ID != first.ID {
		t.Fatalf("repeated create returned ID %q, want persisted ID %q", persistedSecond.ID, first.ID)
	}
	if second.ID == first.ID {
		t.Fatal("DAO mutated the repeated-create input ID")
	}
	persisted, err := dao.GetByName(ctx, name, "az-test")
	if err != nil {
		t.Fatalf("load persisted PCCN: %v", err)
	}
	if persisted.ID != first.ID {
		t.Fatalf("persisted ID = %q, want %q", persisted.ID, first.ID)
	}
}

func newPCCNForCreate(id, name string) *models.PCCNResource {
	return &models.PCCNResource{
		ID:            id,
		PCCNName:      name,
		VPCName:       "vpc-a",
		VPCRegion:     "region-a",
		PeerVPCName:   "vpc-b",
		PeerVPCRegion: "region-b",
		AZ:            "az-test",
		Status:        models.ResourceStatusPending,
		Subnets:       []string{"10.0.0.0/24"},
	}
}
