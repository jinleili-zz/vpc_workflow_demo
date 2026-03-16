package dao

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"net"
	"time"

	"workflow_qoder/internal/models"

	"github.com/google/uuid"
)

type TopVPCDAO struct {
	db *sql.DB
}

func NewTopVPCDAO(db *sql.DB) *TopVPCDAO {
	return &TopVPCDAO{db: db}
}

func (d *TopVPCDAO) RegisterVPC(ctx context.Context, vpc *models.VPCRegistry) error {
	azDetailsJSON, err := json.Marshal(vpc.AZDetails)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO vpc_registry (id, vpc_name, region, vrf_name, vlan_id, firewall_zone, status, saga_tx_id, az_details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (vpc_name) DO UPDATE SET
		vrf_name = EXCLUDED.vrf_name,
		vlan_id = EXCLUDED.vlan_id,
		firewall_zone = EXCLUDED.firewall_zone,
		status = EXCLUDED.status,
		saga_tx_id = EXCLUDED.saga_tx_id,
		az_details = EXCLUDED.az_details,
		updated_at = CURRENT_TIMESTAMP
	`
	_, err = d.db.ExecContext(ctx, query,
		vpc.ID, vpc.VPCName, vpc.Region,
		vpc.VRFName, vpc.VLANId, vpc.FirewallZone, vpc.Status, vpc.SagaTxID, azDetailsJSON,
	)
	return err
}

func (d *TopVPCDAO) UpdateVPCOverallStatus(ctx context.Context, vpcName, status string, azDetails map[string]models.AZDetail) error {
	azDetailsJSON, err := json.Marshal(azDetails)
	if err != nil {
		return err
	}
	query := `UPDATE vpc_registry SET status = $1, az_details = $2, updated_at = NOW() WHERE vpc_name = $3`
	_, err = d.db.ExecContext(ctx, query, status, azDetailsJSON, vpcName)
	return err
}

// GetVPCByName 根据 vpc_name 查询唯一的 VPC 记录
func (d *TopVPCDAO) GetVPCByName(ctx context.Context, vpcName string) (*models.VPCRegistry, error) {
	query := `
		SELECT id, vpc_name, region, vrf_name, vlan_id, firewall_zone,
		       status, saga_tx_id, az_details, created_at, updated_at
		FROM vpc_registry WHERE vpc_name = $1
	`
	vpc := &models.VPCRegistry{}
	var azDetailsJSON []byte
	err := d.db.QueryRowContext(ctx, query, vpcName).Scan(
		&vpc.ID, &vpc.VPCName, &vpc.Region,
		&vpc.VRFName, &vpc.VLANId, &vpc.FirewallZone, &vpc.Status,
		&vpc.SagaTxID, &azDetailsJSON, &vpc.CreatedAt, &vpc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if len(azDetailsJSON) > 0 {
		json.Unmarshal(azDetailsJSON, &vpc.AZDetails)
	}
	if vpc.AZDetails == nil {
		vpc.AZDetails = make(map[string]models.AZDetail)
	}
	return vpc, nil
}

func (d *TopVPCDAO) GetVPCsByZone(ctx context.Context, zone string) ([]*models.VPCRegistry, error) {
	query := `
		SELECT id, vpc_name, region, vrf_name, vlan_id, firewall_zone,
		       status, saga_tx_id, az_details, created_at, updated_at
		FROM vpc_registry WHERE firewall_zone = $1 AND status = 'running'
	`
	return d.scanVPCRows(ctx, query, zone)
}

func (d *TopVPCDAO) DeleteVPC(ctx context.Context, vpcName string) error {
	query := `DELETE FROM vpc_registry WHERE vpc_name = $1`
	_, err := d.db.ExecContext(ctx, query, vpcName)
	return err
}

func (d *TopVPCDAO) RegisterSubnet(ctx context.Context, subnet *models.SubnetRegistry) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
		INSERT INTO subnet_registry (id, subnet_name, vpc_name, region, az, az_subnet_id, cidr, firewall_zone, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (subnet_name, az) DO UPDATE SET
		az_subnet_id = EXCLUDED.az_subnet_id,
		cidr = EXCLUDED.cidr,
		firewall_zone = EXCLUDED.firewall_zone,
		status = EXCLUDED.status,
		updated_at = CURRENT_TIMESTAMP
	`
	_, err = tx.ExecContext(ctx, query,
		subnet.ID, subnet.SubnetName, subnet.VPCName, subnet.Region, subnet.AZ,
		subnet.AZSubnetID, subnet.CIDR, subnet.FirewallZone, subnet.Status,
	)
	if err != nil {
		return err
	}

	start, end := cidrToRange(subnet.CIDR)
	mappingQuery := `
		INSERT INTO cidr_zone_mapping (id, cidr, cidr_start, cidr_end, vpc_name, subnet_name, region, az, firewall_zone)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (cidr, az) DO UPDATE SET
		cidr_start = EXCLUDED.cidr_start,
		cidr_end = EXCLUDED.cidr_end,
		firewall_zone = EXCLUDED.firewall_zone
	`
	_, err = tx.ExecContext(ctx, mappingQuery,
		uuid.New().String(), subnet.CIDR, start, end,
		subnet.VPCName, subnet.SubnetName, subnet.Region, subnet.AZ, subnet.FirewallZone,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (d *TopVPCDAO) UpdateSubnetStatus(ctx context.Context, subnetName, az, status string) error {
	query := `UPDATE subnet_registry SET status = $1, updated_at = $2 WHERE subnet_name = $3 AND az = $4`
	_, err := d.db.ExecContext(ctx, query, status, time.Now(), subnetName, az)
	return err
}

func (d *TopVPCDAO) DeleteSubnet(ctx context.Context, subnetName, az string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var cidr string
	err = tx.QueryRowContext(ctx, `SELECT cidr FROM subnet_registry WHERE subnet_name = $1 AND az = $2`, subnetName, az).Scan(&cidr)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	if cidr != "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM cidr_zone_mapping WHERE cidr = $1 AND az = $2`, cidr, az)
		if err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM subnet_registry WHERE subnet_name = $1 AND az = $2`, subnetName, az)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (d *TopVPCDAO) FindZoneByIP(ctx context.Context, ipStr string) (*models.ZoneInfo, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, nil
	}
	ip = ip.To4()
	if ip == nil {
		return nil, nil
	}

	ipNum := uint64(binary.BigEndian.Uint32(ip))

	query := `
		SELECT vpc_name, subnet_name, region, az, firewall_zone, cidr
		FROM cidr_zone_mapping
		WHERE cidr_start <= $1 AND cidr_end >= $2
		ORDER BY (cidr_end - cidr_start) ASC
		LIMIT 1
	`
	var info models.ZoneInfo
	err := d.db.QueryRowContext(ctx, query, ipNum, ipNum).Scan(
		&info.VPCName, &info.SubnetName, &info.Region, &info.AZ, &info.FirewallZone, &info.CIDR,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func (d *TopVPCDAO) ListAllVPCs(ctx context.Context) ([]*models.VPCRegistry, error) {
	query := `
		SELECT id, vpc_name, region, vrf_name, vlan_id, firewall_zone,
		       status, saga_tx_id, az_details, created_at, updated_at
		FROM vpc_registry WHERE status != 'deleted'
		ORDER BY created_at DESC
	`
	return d.scanVPCRows(ctx, query)
}

// scanVPCRows is a helper to scan multiple VPC rows from a query result.
func (d *TopVPCDAO) scanVPCRows(ctx context.Context, query string, args ...any) ([]*models.VPCRegistry, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vpcs []*models.VPCRegistry
	for rows.Next() {
		vpc := &models.VPCRegistry{}
		var azDetailsJSON []byte
		if err := rows.Scan(
			&vpc.ID, &vpc.VPCName, &vpc.Region,
			&vpc.VRFName, &vpc.VLANId, &vpc.FirewallZone, &vpc.Status,
			&vpc.SagaTxID, &azDetailsJSON, &vpc.CreatedAt, &vpc.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if len(azDetailsJSON) > 0 {
			json.Unmarshal(azDetailsJSON, &vpc.AZDetails)
		}
		if vpc.AZDetails == nil {
			vpc.AZDetails = make(map[string]models.AZDetail)
		}
		vpcs = append(vpcs, vpc)
	}
	return vpcs, rows.Err()
}

func cidrToRange(cidrStr string) (uint64, uint64) {
	_, ipNet, err := net.ParseCIDR(cidrStr)
	if err != nil {
		return 0, 0
	}

	start := binary.BigEndian.Uint32(ipNet.IP.To4())
	ones, bits := ipNet.Mask.Size()
	hostBits := uint32(bits - ones)
	end := start | ((1 << hostBits) - 1)

	return uint64(start), uint64(end)
}
