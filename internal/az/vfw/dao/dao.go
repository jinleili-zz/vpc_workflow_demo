package dao

import (
	"context"
	"database/sql"
	"time"

	"workflow_qoder/internal/models"
)

type FirewallPolicyDAO struct {
	db *sql.DB
}

func NewFirewallPolicyDAO(db *sql.DB) *FirewallPolicyDAO {
	return &FirewallPolicyDAO{db: db}
}

func (d *FirewallPolicyDAO) Create(ctx context.Context, policy *models.FirewallPolicy) error {
	query := `
		INSERT INTO firewall_policies (
			id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			source_port, dest_port, protocol, action, description,
			status, total_tasks, completed_tasks, failed_tasks, region, az
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`
	_, err := d.db.ExecContext(ctx, query,
		policy.ID, policy.PolicyName, policy.SourceZone, policy.DestZone,
		policy.SourceIP, policy.DestIP, policy.SourcePort, policy.DestPort,
		policy.Protocol, policy.Action, policy.Description,
		policy.Status, policy.TotalTasks, policy.CompletedTasks, policy.FailedTasks,
		policy.Region, policy.AZ,
	)
	return err
}

func (d *FirewallPolicyDAO) GetByID(ctx context.Context, id string) (*models.FirewallPolicy, error) {
	query := `
		SELECT id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			   source_port, dest_port, protocol, action, description,
			   status, error_message, total_tasks, completed_tasks, failed_tasks,
			   region, az, created_at, updated_at
		FROM firewall_policies WHERE id = $1
	`
	policy := &models.FirewallPolicy{}
	var desc, errMsg sql.NullString

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&policy.ID, &policy.PolicyName, &policy.SourceZone, &policy.DestZone,
		&policy.SourceIP, &policy.DestIP, &policy.SourcePort, &policy.DestPort,
		&policy.Protocol, &policy.Action, &desc,
		&policy.Status, &errMsg, &policy.TotalTasks, &policy.CompletedTasks, &policy.FailedTasks,
		&policy.Region, &policy.AZ, &policy.CreatedAt, &policy.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if desc.Valid {
		policy.Description = desc.String
	}
	if errMsg.Valid {
		policy.ErrorMessage = errMsg.String
	}

	return policy, nil
}

func (d *FirewallPolicyDAO) GetByName(ctx context.Context, name, az string) (*models.FirewallPolicy, error) {
	query := `
		SELECT id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			   source_port, dest_port, protocol, action, description,
			   status, error_message, total_tasks, completed_tasks, failed_tasks,
			   region, az, created_at, updated_at
		FROM firewall_policies WHERE policy_name = $1 AND az = $2
	`
	policy := &models.FirewallPolicy{}
	var desc, errMsg sql.NullString

	err := d.db.QueryRowContext(ctx, query, name, az).Scan(
		&policy.ID, &policy.PolicyName, &policy.SourceZone, &policy.DestZone,
		&policy.SourceIP, &policy.DestIP, &policy.SourcePort, &policy.DestPort,
		&policy.Protocol, &policy.Action, &desc,
		&policy.Status, &errMsg, &policy.TotalTasks, &policy.CompletedTasks, &policy.FailedTasks,
		&policy.Region, &policy.AZ, &policy.CreatedAt, &policy.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if desc.Valid {
		policy.Description = desc.String
	}
	if errMsg.Valid {
		policy.ErrorMessage = errMsg.String
	}

	return policy, nil
}

func (d *FirewallPolicyDAO) UpdateStatus(ctx context.Context, id string, status models.ResourceStatus, errorMsg string) error {
	query := `UPDATE firewall_policies SET status = $1, error_message = $2, updated_at = $3 WHERE id = $4`
	_, err := d.db.ExecContext(ctx, query, status, errorMsg, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) UpdateTotalTasks(ctx context.Context, id string, totalTasks int) error {
	query := `UPDATE firewall_policies SET total_tasks = $1, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, totalTasks, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) IncrementCompletedTasks(ctx context.Context, id string) error {
	query := `UPDATE firewall_policies SET completed_tasks = completed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) IncrementFailedTasks(ctx context.Context, id string) error {
	query := `UPDATE firewall_policies SET failed_tasks = failed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM firewall_policies WHERE id = $1`
	_, err := d.db.ExecContext(ctx, query, id)
	return err
}

func (d *FirewallPolicyDAO) ListAll(ctx context.Context) ([]*models.FirewallPolicy, error) {
	query := `
		SELECT id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			   source_port, dest_port, protocol, action, description,
			   status, error_message, total_tasks, completed_tasks, failed_tasks,
			   region, az, created_at, updated_at
		FROM firewall_policies
		WHERE status != 'deleted'
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []*models.FirewallPolicy
	for rows.Next() {
		policy := &models.FirewallPolicy{}
		var desc, errMsg sql.NullString

		err := rows.Scan(
			&policy.ID, &policy.PolicyName, &policy.SourceZone, &policy.DestZone,
			&policy.SourceIP, &policy.DestIP, &policy.SourcePort, &policy.DestPort,
			&policy.Protocol, &policy.Action, &desc,
			&policy.Status, &errMsg, &policy.TotalTasks, &policy.CompletedTasks, &policy.FailedTasks,
			&policy.Region, &policy.AZ, &policy.CreatedAt, &policy.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if desc.Valid {
			policy.Description = desc.String
		}
		if errMsg.Valid {
			policy.ErrorMessage = errMsg.String
		}

		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (d *FirewallPolicyDAO) CountByZone(ctx context.Context, zone string) (int, error) {
	query := `
		SELECT COUNT(*) FROM firewall_policies 
		WHERE (source_zone = $1 OR dest_zone = $2) AND status NOT IN ('deleted', 'failed')
	`
	var count int
	err := d.db.QueryRowContext(ctx, query, zone, zone).Scan(&count)
	return count, err
}
