package dao

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"
)

type TopVFWDAO struct {
	db *sql.DB
}

func NewTopVFWDAO(db *sql.DB) *TopVFWDAO {
	return &TopVFWDAO{db: db}
}

func (d *TopVFWDAO) CreatePolicy(ctx context.Context, policy *models.PolicyRegistry) error {
	query := `
		INSERT INTO policy_registry (
			id, policy_name, source_ip, dest_ip, source_port, dest_port, protocol, action, description,
			source_vpc, dest_vpc, source_zone, dest_zone, source_region, dest_region, source_az, dest_az, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (id) DO NOTHING
	`
	_, err := d.db.ExecContext(ctx, query,
		policy.ID, policy.PolicyName, policy.SourceIP, policy.DestIP,
		policy.SourcePort, policy.DestPort, policy.Protocol, policy.Action, policy.Description,
		policy.SourceVPC, policy.DestVPC, policy.SourceZone, policy.DestZone,
		policy.SourceRegion, policy.DestRegion, policy.SourceAZ, policy.DestAZ, policy.Status,
	)
	return err
}

func (d *TopVFWDAO) GetPolicyByID(ctx context.Context, id string) (*models.PolicyRegistry, error) {
	query := `
		SELECT id, policy_name, source_ip, dest_ip, source_port, dest_port, protocol, action, description,
			   source_vpc, dest_vpc, source_zone, dest_zone, source_region, dest_region, source_az, dest_az,
			   status, error_message, created_at, updated_at
		FROM policy_registry WHERE id = $1
	`
	policy := &models.PolicyRegistry{}
	var desc, srcVPC, dstVPC, srcZone, dstZone, srcRegion, dstRegion, srcAZ, dstAZ, errMsg sql.NullString

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&policy.ID, &policy.PolicyName, &policy.SourceIP, &policy.DestIP,
		&policy.SourcePort, &policy.DestPort, &policy.Protocol, &policy.Action, &desc,
		&srcVPC, &dstVPC, &srcZone, &dstZone, &srcRegion, &dstRegion, &srcAZ, &dstAZ,
		&policy.Status, &errMsg, &policy.CreatedAt, &policy.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if desc.Valid {
		policy.Description = desc.String
	}
	if srcVPC.Valid {
		policy.SourceVPC = srcVPC.String
	}
	if dstVPC.Valid {
		policy.DestVPC = dstVPC.String
	}
	if srcZone.Valid {
		policy.SourceZone = srcZone.String
	}
	if dstZone.Valid {
		policy.DestZone = dstZone.String
	}
	if srcRegion.Valid {
		policy.SourceRegion = srcRegion.String
	}
	if dstRegion.Valid {
		policy.DestRegion = dstRegion.String
	}
	if srcAZ.Valid {
		policy.SourceAZ = srcAZ.String
	}
	if dstAZ.Valid {
		policy.DestAZ = dstAZ.String
	}
	if errMsg.Valid {
		policy.ErrorMessage = errMsg.String
	}

	return policy, nil
}

func (d *TopVFWDAO) GetPolicyByName(ctx context.Context, name string) (*models.PolicyRegistry, error) {
	query := `
		SELECT id, policy_name, source_ip, dest_ip, source_port, dest_port, protocol, action, description,
			   source_vpc, dest_vpc, source_zone, dest_zone, source_region, dest_region, source_az, dest_az,
			   status, error_message, created_at, updated_at
		FROM policy_registry WHERE policy_name = $1
	`
	policy := &models.PolicyRegistry{}
	var desc, srcVPC, dstVPC, srcZone, dstZone, srcRegion, dstRegion, srcAZ, dstAZ, errMsg sql.NullString

	err := d.db.QueryRowContext(ctx, query, name).Scan(
		&policy.ID, &policy.PolicyName, &policy.SourceIP, &policy.DestIP,
		&policy.SourcePort, &policy.DestPort, &policy.Protocol, &policy.Action, &desc,
		&srcVPC, &dstVPC, &srcZone, &dstZone, &srcRegion, &dstRegion, &srcAZ, &dstAZ,
		&policy.Status, &errMsg, &policy.CreatedAt, &policy.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if desc.Valid {
		policy.Description = desc.String
	}
	if srcVPC.Valid {
		policy.SourceVPC = srcVPC.String
	}
	if dstVPC.Valid {
		policy.DestVPC = dstVPC.String
	}
	if srcZone.Valid {
		policy.SourceZone = srcZone.String
	}
	if dstZone.Valid {
		policy.DestZone = dstZone.String
	}
	if srcRegion.Valid {
		policy.SourceRegion = srcRegion.String
	}
	if dstRegion.Valid {
		policy.DestRegion = dstRegion.String
	}
	if srcAZ.Valid {
		policy.SourceAZ = srcAZ.String
	}
	if dstAZ.Valid {
		policy.DestAZ = dstAZ.String
	}
	if errMsg.Valid {
		policy.ErrorMessage = errMsg.String
	}

	return policy, nil
}

func (d *TopVFWDAO) UpdatePolicyStatus(ctx context.Context, id, status, errorMsg string) error {
	query := `UPDATE policy_registry SET status = $1, error_message = $2, updated_at = $3 WHERE id = $4`
	_, err := d.db.ExecContext(ctx, query, status, errorMsg, time.Now(), id)
	return err
}

func (d *TopVFWDAO) DeletePolicy(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `DELETE FROM policy_az_records WHERE policy_id = $1`, id)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM policy_registry WHERE id = $1`, id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (d *TopVFWDAO) DeletePolicyAndReleaseTarget(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var policyName string
	if err := tx.QueryRowContext(ctx, `SELECT policy_name FROM policy_registry WHERE id = $1 FOR UPDATE`, id).Scan(&policyName); err != nil {
		if err == sql.ErrNoRows {
			return tx.Commit()
		}
		return err
	}
	var createStatus operation.Status
	err = tx.QueryRowContext(ctx, `
		SELECT operation.status
		FROM orchestration_target_claims claim
		JOIN orchestration_operations operation ON operation.operation_id = claim.operation_id
		WHERE claim.owner_service = 'top-nsp-vfw' AND claim.resource_type = 'firewall_policy'
		  AND claim.target_scope = $1 AND claim.active = TRUE
		FOR UPDATE OF claim, operation
	`, policyName).Scan(&createStatus)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && !createStatus.Terminal() {
		return fmt.Errorf("%w: target create operation is %s", operation.ErrResourceOperationInProgress, createStatus)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM policy_az_records WHERE policy_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM policy_registry WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE orchestration_target_claims SET active = FALSE, retiring = FALSE, updated_at = NOW()
		WHERE owner_service = 'top-nsp-vfw' AND resource_type = 'firewall_policy' AND target_scope = $1 AND active = TRUE
	`, policyName); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *TopVFWDAO) ListPolicies(ctx context.Context) ([]*models.PolicyRegistry, error) {
	query := `
		SELECT id, policy_name, source_ip, dest_ip, source_port, dest_port, protocol, action, description,
			   source_vpc, dest_vpc, source_zone, dest_zone, source_region, dest_region, source_az, dest_az,
			   status, error_message, created_at, updated_at
		FROM policy_registry
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []*models.PolicyRegistry
	for rows.Next() {
		policy := &models.PolicyRegistry{}
		var desc, srcVPC, dstVPC, srcZone, dstZone, srcRegion, dstRegion, srcAZ, dstAZ, errMsg sql.NullString

		err := rows.Scan(
			&policy.ID, &policy.PolicyName, &policy.SourceIP, &policy.DestIP,
			&policy.SourcePort, &policy.DestPort, &policy.Protocol, &policy.Action, &desc,
			&srcVPC, &dstVPC, &srcZone, &dstZone, &srcRegion, &dstRegion, &srcAZ, &dstAZ,
			&policy.Status, &errMsg, &policy.CreatedAt, &policy.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if desc.Valid {
			policy.Description = desc.String
		}
		if srcVPC.Valid {
			policy.SourceVPC = srcVPC.String
		}
		if dstVPC.Valid {
			policy.DestVPC = dstVPC.String
		}
		if srcZone.Valid {
			policy.SourceZone = srcZone.String
		}
		if dstZone.Valid {
			policy.DestZone = dstZone.String
		}
		if srcRegion.Valid {
			policy.SourceRegion = srcRegion.String
		}
		if dstRegion.Valid {
			policy.DestRegion = dstRegion.String
		}
		if srcAZ.Valid {
			policy.SourceAZ = srcAZ.String
		}
		if dstAZ.Valid {
			policy.DestAZ = dstAZ.String
		}
		if errMsg.Valid {
			policy.ErrorMessage = errMsg.String
		}

		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (d *TopVFWDAO) CreateAZRecord(ctx context.Context, record *models.PolicyAZRecord) error {
	query := `
		INSERT INTO policy_az_records (id, policy_id, region, az, az_policy_id, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (policy_id, region, az) DO NOTHING
	`
	_, err := d.db.ExecContext(ctx, query,
		record.ID, record.PolicyID, record.Region, record.AZ, record.AZPolicyID, record.Status,
	)
	return err
}

func (d *TopVFWDAO) UpdateAZRecord(ctx context.Context, policyID, region, az, azPolicyID, status, errorMsg string) error {
	query := `UPDATE policy_az_records SET az_policy_id = $1, status = $2, error_message = $3, updated_at = $4 WHERE policy_id = $5 AND region = $6 AND az = $7`
	_, err := d.db.ExecContext(ctx, query, azPolicyID, status, errorMsg, time.Now(), policyID, region, az)
	return err
}

func (d *TopVFWDAO) GetAZRecords(ctx context.Context, policyID string) ([]*models.PolicyAZRecord, error) {
	query := `
		SELECT id, policy_id, region, az, az_policy_id, status, error_message, created_at, updated_at
		FROM policy_az_records WHERE policy_id = $1
	`
	rows, err := d.db.QueryContext(ctx, query, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*models.PolicyAZRecord
	for rows.Next() {
		record := &models.PolicyAZRecord{}
		var azPolicyID, errMsg sql.NullString
		err := rows.Scan(
			&record.ID, &record.PolicyID, &record.Region, &record.AZ, &azPolicyID,
			&record.Status, &errMsg, &record.CreatedAt, &record.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		if azPolicyID.Valid {
			record.AZPolicyID = azPolicyID.String
		}
		if errMsg.Valid {
			record.ErrorMessage = errMsg.String
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (d *TopVFWDAO) CountPoliciesByZone(ctx context.Context, zone string) (int, error) {
	query := `
		SELECT COUNT(*) FROM policy_registry 
		WHERE (source_zone = $1 OR dest_zone = $2) AND status <> 'deleted'
	`
	var count int
	err := d.db.QueryRowContext(ctx, query, zone, zone).Scan(&count)
	return count, err
}
