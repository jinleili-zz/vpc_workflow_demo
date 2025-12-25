package dao

import (
	"context"
	"database/sql"
	"time"

	"workflow_qoder/internal/models"
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
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		FROM policy_registry WHERE id = ?
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
		FROM policy_registry WHERE policy_name = ?
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
	query := `UPDATE policy_registry SET status = ?, error_message = ?, updated_at = ? WHERE id = ?`
	_, err := d.db.ExecContext(ctx, query, status, errorMsg, time.Now(), id)
	return err
}

func (d *TopVFWDAO) DeletePolicy(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `DELETE FROM policy_az_records WHERE policy_id = ?`, id)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM policy_registry WHERE id = ?`, id)
	if err != nil {
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
		INSERT INTO policy_az_records (id, policy_id, az, az_policy_id, status)
		VALUES (?, ?, ?, ?, ?)
	`
	_, err := d.db.ExecContext(ctx, query,
		record.ID, record.PolicyID, record.AZ, record.AZPolicyID, record.Status,
	)
	return err
}

func (d *TopVFWDAO) UpdateAZRecord(ctx context.Context, policyID, az, azPolicyID, status, errorMsg string) error {
	query := `UPDATE policy_az_records SET az_policy_id = ?, status = ?, error_message = ?, updated_at = ? WHERE policy_id = ? AND az = ?`
	_, err := d.db.ExecContext(ctx, query, azPolicyID, status, errorMsg, time.Now(), policyID, az)
	return err
}

func (d *TopVFWDAO) GetAZRecords(ctx context.Context, policyID string) ([]*models.PolicyAZRecord, error) {
	query := `
		SELECT id, policy_id, az, az_policy_id, status, error_message, created_at, updated_at
		FROM policy_az_records WHERE policy_id = ?
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
			&record.ID, &record.PolicyID, &record.AZ, &azPolicyID,
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
		WHERE (source_zone = ? OR dest_zone = ?) AND status NOT IN ('deleted', 'failed')
	`
	var count int
	err := d.db.QueryRowContext(ctx, query, zone, zone).Scan(&count)
	return count, err
}
