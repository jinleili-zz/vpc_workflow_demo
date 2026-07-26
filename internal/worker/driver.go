package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// DeviceTarget is the stable identity passed from the AZ workflow to a device
// driver. Real drivers should map TargetKey to a deterministic device object
// name and pass OperationKey through as the vendor idempotency token when the
// device API supports one.
type DeviceTarget struct {
	OperationKey string
	DeviceType   string
	TargetKey    string
	Action       string
	DesiredHash  string
	ResourceType string
	ResourceID   string
	Generation   int64
}

type ActualState struct {
	Exists      bool
	DesiredHash string
}

// DeviceDriver isolates vendor-specific query/reconcile semantics from queue
// handling. Ensure methods must be safe to call again after an ambiguous
// timeout.
type DeviceDriver interface {
	Get(context.Context, DeviceTarget) (ActualState, error)
	Compare(ActualState, DeviceTarget) bool
	EnsurePresent(context.Context, DeviceTarget, json.RawMessage) error
	EnsureAbsent(context.Context, DeviceTarget) error
}

type sqlDeviceDriver struct {
	db *sql.DB
}

func newSQLDeviceDriver(db *sql.DB) DeviceDriver {
	return &sqlDeviceDriver{db: db}
}

func (d *sqlDeviceDriver) Get(ctx context.Context, target DeviceTarget) (ActualState, error) {
	if d == nil || d.db == nil {
		return ActualState{}, fmt.Errorf("device state database is required")
	}
	if isEnsureAbsent(target.Action) {
		var exists bool
		err := d.db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM worker_device_state
				WHERE device_type = $1 AND target_key = $2
			)
		`, target.DeviceType, target.TargetKey).Scan(&exists)
		return ActualState{Exists: exists}, err
	}
	var desiredHash string
	err := d.db.QueryRowContext(ctx, `
		SELECT desired_hash FROM worker_device_state WHERE operation_key = $1
	`, target.OperationKey).Scan(&desiredHash)
	if err == sql.ErrNoRows {
		return ActualState{}, nil
	}
	if err != nil {
		return ActualState{}, err
	}
	return ActualState{Exists: true, DesiredHash: desiredHash}, nil
}

func (d *sqlDeviceDriver) Compare(actual ActualState, target DeviceTarget) bool {
	if isEnsureAbsent(target.Action) {
		return !actual.Exists
	}
	return actual.Exists && actual.DesiredHash == target.DesiredHash
}

func (d *sqlDeviceDriver) EnsurePresent(ctx context.Context, target DeviceTarget, result json.RawMessage) error {
	command, err := d.db.ExecContext(ctx, `
		INSERT INTO worker_device_state (
			operation_key, device_type, target_key, desired_hash, result_payload
		) VALUES ($1,$2,$3,$4,$5::jsonb)
		ON CONFLICT (operation_key) DO UPDATE
		SET result_payload = EXCLUDED.result_payload, updated_at = NOW()
		WHERE worker_device_state.desired_hash = EXCLUDED.desired_hash
	`, target.OperationKey, target.DeviceType, target.TargetKey, target.DesiredHash, result)
	if err != nil {
		return err
	}
	if rows, _ := command.RowsAffected(); rows != 1 {
		return ErrDesiredConflict
	}
	return nil
}

func (d *sqlDeviceDriver) EnsureAbsent(ctx context.Context, target DeviceTarget) error {
	_, err := d.db.ExecContext(ctx, `
		DELETE FROM worker_device_state
		WHERE device_type = $1 AND target_key = $2
	`, target.DeviceType, target.TargetKey)
	return err
}

func isEnsureAbsent(action string) bool {
	return strings.HasPrefix(strings.ToLower(action), "delete_") ||
		strings.HasPrefix(strings.ToLower(action), "remove_")
}
