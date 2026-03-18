package dao

import (
	"context"
	"database/sql"
	"encoding/json"

	"workflow_qoder/internal/models"

	"github.com/google/uuid"
)

type TopPCCNDAO struct {
	db *sql.DB
}

func NewTopPCCNDAO(db *sql.DB) *TopPCCNDAO {
	return &TopPCCNDAO{db: db}
}

// RegisterPCCN registers a new PCCN in the Top layer registry
func (d *TopPCCNDAO) RegisterPCCN(ctx context.Context, pccn *models.PCCNRegistry) error {
	vpcDetailsJSON, err := json.Marshal(pccn.VPCDetails)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO pccn_registry (id, pccn_name, vpc1_name, vpc1_region, vpc2_name, vpc2_region, status, saga_tx_id, vpc_details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (pccn_name) DO UPDATE SET
			vpc1_name = EXCLUDED.vpc1_name,
			vpc1_region = EXCLUDED.vpc1_region,
			vpc2_name = EXCLUDED.vpc2_name,
			vpc2_region = EXCLUDED.vpc2_region,
			status = EXCLUDED.status,
			saga_tx_id = EXCLUDED.saga_tx_id,
			vpc_details = EXCLUDED.vpc_details,
			updated_at = CURRENT_TIMESTAMP
	`
	_, err = d.db.ExecContext(ctx, query,
		pccn.ID, pccn.PCCNName, pccn.VPC1Name, pccn.VPC1Region, pccn.VPC2Name, pccn.VPC2Region,
		pccn.Status, pccn.TxID, vpcDetailsJSON,
	)
	return err
}

// GetPCCNByName retrieves a PCCN by name
func (d *TopPCCNDAO) GetPCCNByName(ctx context.Context, pccnName string) (*models.PCCNRegistry, error) {
	query := `
		SELECT id, pccn_name, vpc1_name, vpc1_region, vpc2_name, vpc2_region,
		       status, saga_tx_id, vpc_details, created_at, updated_at
		FROM pccn_registry
		WHERE pccn_name = $1
	`
	pccn := &models.PCCNRegistry{}
	var vpcDetailsJSON []byte
	var txID sql.NullString

	err := d.db.QueryRowContext(ctx, query, pccnName).Scan(
		&pccn.ID, &pccn.PCCNName, &pccn.VPC1Name, &pccn.VPC1Region, &pccn.VPC2Name, &pccn.VPC2Region,
		&pccn.Status, &txID, &vpcDetailsJSON, &pccn.CreatedAt, &pccn.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if txID.Valid {
		pccn.TxID = txID.String
	}

	if len(vpcDetailsJSON) > 0 {
		json.Unmarshal(vpcDetailsJSON, &pccn.VPCDetails)
	}
	if pccn.VPCDetails == nil {
		pccn.VPCDetails = make(map[string]models.VPCDetail)
	}

	return pccn, nil
}

// GetPCCNByID retrieves a PCCN by ID
func (d *TopPCCNDAO) GetPCCNByID(ctx context.Context, pccnID string) (*models.PCCNRegistry, error) {
	query := `
		SELECT id, pccn_name, vpc1_name, vpc1_region, vpc2_name, vpc2_region,
		       status, saga_tx_id, vpc_details, created_at, updated_at
		FROM pccn_registry
		WHERE id = $1
	`
	pccn := &models.PCCNRegistry{}
	var vpcDetailsJSON []byte
	var txID sql.NullString

	err := d.db.QueryRowContext(ctx, query, pccnID).Scan(
		&pccn.ID, &pccn.PCCNName, &pccn.VPC1Name, &pccn.VPC1Region, &pccn.VPC2Name, &pccn.VPC2Region,
		&pccn.Status, &txID, &vpcDetailsJSON, &pccn.CreatedAt, &pccn.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if txID.Valid {
		pccn.TxID = txID.String
	}

	if len(vpcDetailsJSON) > 0 {
		json.Unmarshal(vpcDetailsJSON, &pccn.VPCDetails)
	}
	if pccn.VPCDetails == nil {
		pccn.VPCDetails = make(map[string]models.VPCDetail)
	}

	return pccn, nil
}

// UpdatePCCNStatus updates the PCCN status and VPC details
func (d *TopPCCNDAO) UpdatePCCNStatus(ctx context.Context, pccnName, status string, vpcDetails map[string]models.VPCDetail) error {
	vpcDetailsJSON, err := json.Marshal(vpcDetails)
	if err != nil {
		return err
	}

	query := `UPDATE pccn_registry SET status = $1, vpc_details = $2, updated_at = NOW() WHERE pccn_name = $3`
	_, err = d.db.ExecContext(ctx, query, status, vpcDetailsJSON, pccnName)
	return err
}

// UpdatePCCNTxID updates the Saga transaction ID
func (d *TopPCCNDAO) UpdatePCCNTxID(ctx context.Context, pccnName, txID string) error {
	query := `UPDATE pccn_registry SET saga_tx_id = $1, updated_at = NOW() WHERE pccn_name = $2`
	_, err := d.db.ExecContext(ctx, query, txID, pccnName)
	return err
}

// DeletePCCN deletes a PCCN from the registry
func (d *TopPCCNDAO) DeletePCCN(ctx context.Context, pccnName string) error {
	query := `DELETE FROM pccn_registry WHERE pccn_name = $1`
	_, err := d.db.ExecContext(ctx, query, pccnName)
	return err
}

// ListAllPCCNs lists all PCCNs
func (d *TopPCCNDAO) ListAllPCCNs(ctx context.Context) ([]*models.PCCNRegistry, error) {
	query := `
		SELECT id, pccn_name, vpc1_name, vpc1_region, vpc2_name, vpc2_region,
		       status, saga_tx_id, vpc_details, created_at, updated_at
		FROM pccn_registry
		WHERE status != 'deleted'
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pccns []*models.PCCNRegistry
	for rows.Next() {
		pccn := &models.PCCNRegistry{}
		var vpcDetailsJSON []byte
		var txID sql.NullString

		err := rows.Scan(
			&pccn.ID, &pccn.PCCNName, &pccn.VPC1Name, &pccn.VPC1Region, &pccn.VPC2Name, &pccn.VPC2Region,
			&pccn.Status, &txID, &vpcDetailsJSON, &pccn.CreatedAt, &pccn.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if txID.Valid {
			pccn.TxID = txID.String
		}

		if len(vpcDetailsJSON) > 0 {
			json.Unmarshal(vpcDetailsJSON, &pccn.VPCDetails)
		}
		if pccn.VPCDetails == nil {
			pccn.VPCDetails = make(map[string]models.VPCDetail)
		}

		pccns = append(pccns, pccn)
	}
	return pccns, rows.Err()
}

// GeneratePCCNID generates a new unique PCCN ID
func GeneratePCCNID() string {
	return uuid.New().String()
}

// GetPCCNsByVPCName retrieves all PCCNs involving a specific VPC
func (d *TopPCCNDAO) GetPCCNsByVPCName(ctx context.Context, vpcName string) ([]*models.PCCNRegistry, error) {
	query := `
		SELECT id, pccn_name, vpc1_name, vpc1_region, vpc2_name, vpc2_region,
		       status, saga_tx_id, vpc_details, created_at, updated_at
		FROM pccn_registry
		WHERE (vpc1_name = $1 OR vpc2_name = $1) AND status != 'deleted'
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query, vpcName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pccns []*models.PCCNRegistry
	for rows.Next() {
		pccn := &models.PCCNRegistry{}
		var vpcDetailsJSON []byte
		var txID sql.NullString

		err := rows.Scan(
			&pccn.ID, &pccn.PCCNName, &pccn.VPC1Name, &pccn.VPC1Region, &pccn.VPC2Name, &pccn.VPC2Region,
			&pccn.Status, &txID, &vpcDetailsJSON, &pccn.CreatedAt, &pccn.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if txID.Valid {
			pccn.TxID = txID.String
		}

		if len(vpcDetailsJSON) > 0 {
			json.Unmarshal(vpcDetailsJSON, &pccn.VPCDetails)
		}
		if pccn.VPCDetails == nil {
			pccn.VPCDetails = make(map[string]models.VPCDetail)
		}

		pccns = append(pccns, pccn)
	}
	return pccns, rows.Err()
}
