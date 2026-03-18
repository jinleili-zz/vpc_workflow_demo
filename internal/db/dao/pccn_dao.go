package dao

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"workflow_qoder/internal/models"
)

type PCCNDAO struct {
	db *sql.DB
}

func NewPCCNDAO(db *sql.DB) *PCCNDAO {
	return &PCCNDAO{db: db}
}

func (d *PCCNDAO) Create(ctx context.Context, pccn *models.PCCNResource) error {
	subnetsJSON, _ := json.Marshal(pccn.Subnets)
	query := `
		INSERT INTO pccn_resources (
			id, pccn_name, vpc_name, vpc_region, peer_vpc_name, peer_vpc_region, az,
			status, subnets, total_tasks, completed_tasks, failed_tasks
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`
	_, err := d.db.ExecContext(ctx, query,
		pccn.ID, pccn.PCCNName, pccn.VPCName, pccn.VPCRegion, pccn.PeerVPCName, pccn.PeerVPCRegion, pccn.AZ,
		pccn.Status, subnetsJSON, pccn.TotalTasks, pccn.CompletedTasks, pccn.FailedTasks,
	)
	return err
}

func (d *PCCNDAO) GetByID(ctx context.Context, id string) (*models.PCCNResource, error) {
	query := `
		SELECT id, pccn_name, vpc_name, vpc_region, peer_vpc_name, peer_vpc_region, az,
		       status, subnets, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM pccn_resources
		WHERE id = $1
	`
	pccn := &models.PCCNResource{}
	var subnetsJSON []byte
	var errorMessage sql.NullString

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&pccn.ID, &pccn.PCCNName, &pccn.VPCName, &pccn.VPCRegion, &pccn.PeerVPCName, &pccn.PeerVPCRegion, &pccn.AZ,
		&pccn.Status, &subnetsJSON, &errorMessage, &pccn.TotalTasks, &pccn.CompletedTasks, &pccn.FailedTasks,
		&pccn.CreatedAt, &pccn.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(subnetsJSON) > 0 {
		json.Unmarshal(subnetsJSON, &pccn.Subnets)
	}
	if errorMessage.Valid {
		pccn.ErrorMessage = errorMessage.String
	}

	return pccn, nil
}

func (d *PCCNDAO) GetByName(ctx context.Context, pccnName, az string) (*models.PCCNResource, error) {
	query := `
		SELECT id, pccn_name, vpc_name, vpc_region, peer_vpc_name, peer_vpc_region, az,
		       status, subnets, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM pccn_resources
		WHERE pccn_name = $1 AND az = $2
	`
	pccn := &models.PCCNResource{}
	var subnetsJSON []byte
	var errorMessage sql.NullString

	err := d.db.QueryRowContext(ctx, query, pccnName, az).Scan(
		&pccn.ID, &pccn.PCCNName, &pccn.VPCName, &pccn.VPCRegion, &pccn.PeerVPCName, &pccn.PeerVPCRegion, &pccn.AZ,
		&pccn.Status, &subnetsJSON, &errorMessage, &pccn.TotalTasks, &pccn.CompletedTasks, &pccn.FailedTasks,
		&pccn.CreatedAt, &pccn.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(subnetsJSON) > 0 {
		json.Unmarshal(subnetsJSON, &pccn.Subnets)
	}
	if errorMessage.Valid {
		pccn.ErrorMessage = errorMessage.String
	}

	return pccn, nil
}

func (d *PCCNDAO) UpdateStatus(ctx context.Context, id string, status models.ResourceStatus, errorMsg string) error {
	query := `UPDATE pccn_resources SET status = $1, error_message = $2, updated_at = $3 WHERE id = $4`
	_, err := d.db.ExecContext(ctx, query, status, errorMsg, time.Now(), id)
	return err
}

func (d *PCCNDAO) UpdateTotalTasks(ctx context.Context, id string, totalTasks int) error {
	query := `UPDATE pccn_resources SET total_tasks = $1, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, totalTasks, time.Now(), id)
	return err
}

func (d *PCCNDAO) IncrementCompletedTasks(ctx context.Context, id string) error {
	query := `UPDATE pccn_resources SET completed_tasks = completed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *PCCNDAO) IncrementFailedTasks(ctx context.Context, id string) error {
	query := `UPDATE pccn_resources SET failed_tasks = failed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *PCCNDAO) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM pccn_resources WHERE id = $1`
	_, err := d.db.ExecContext(ctx, query, id)
	return err
}

func (d *PCCNDAO) DeleteByName(ctx context.Context, pccnName, az string) error {
	query := `DELETE FROM pccn_resources WHERE pccn_name = $1 AND az = $2`
	_, err := d.db.ExecContext(ctx, query, pccnName, az)
	return err
}

func (d *PCCNDAO) ListAll(ctx context.Context) ([]*models.PCCNResource, error) {
	query := `
		SELECT id, pccn_name, vpc_name, vpc_region, peer_vpc_name, peer_vpc_region, az,
		       status, subnets, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM pccn_resources
		WHERE status != 'deleted'
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pccns []*models.PCCNResource
	for rows.Next() {
		pccn := &models.PCCNResource{}
		var subnetsJSON []byte
		var errorMessage sql.NullString
		err := rows.Scan(
			&pccn.ID, &pccn.PCCNName, &pccn.VPCName, &pccn.VPCRegion, &pccn.PeerVPCName, &pccn.PeerVPCRegion, &pccn.AZ,
			&pccn.Status, &subnetsJSON, &errorMessage, &pccn.TotalTasks, &pccn.CompletedTasks, &pccn.FailedTasks,
			&pccn.CreatedAt, &pccn.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		if len(subnetsJSON) > 0 {
			json.Unmarshal(subnetsJSON, &pccn.Subnets)
		}
		if errorMessage.Valid {
			pccn.ErrorMessage = errorMessage.String
		}
		pccns = append(pccns, pccn)
	}
	return pccns, rows.Err()
}
