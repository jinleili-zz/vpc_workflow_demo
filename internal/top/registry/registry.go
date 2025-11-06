package registry

import (
	"context"
	"fmt"
	"log"
	"time"

	"workflow_qoder/internal/db"
	"workflow_qoder/internal/models"
)

// Registry AZ注册中心
type Registry struct {
}

// NewRegistry 创建注册中心
func NewRegistry() *Registry {
	return &Registry{}
}

// RegisterAZ 注册AZ
func (r *Registry) RegisterAZ(ctx context.Context, az *models.AZ) error {
	// 检查AZ是否已存在
	query := `INSERT INTO az_registry (az_id, region, az_name, nsp_addr, status, last_heartbeat)
	          VALUES (?, ?, ?, ?, 'online', NOW())
	          ON DUPLICATE KEY UPDATE 
	          nsp_addr = VALUES(nsp_addr),
	          status = 'online',
	          last_heartbeat = NOW()`

	_, err := db.GetDB().Exec(query, az.ID, az.Region, az.ID, az.NSPAddr)
	if err != nil {
		return fmt.Errorf("注册AZ失败: %v", err)
	}

	log.Printf("[Registry] AZ注册成功: Region=%s, AZ=%s, Addr=%s", az.Region, az.ID, az.NSPAddr)
	return nil
}

// GetAZ 获取AZ信息
func (r *Registry) GetAZ(ctx context.Context, region, azID string) (*models.AZ, error) {
	query := `SELECT az_id, region, az_name, nsp_addr, status, UNIX_TIMESTAMP(last_heartbeat)
	          FROM az_registry WHERE region = ? AND az_id = ?`

	az := &models.AZ{}
	var lastHeartbeat int64
	err := db.GetDB().QueryRow(query, region, azID).Scan(
		&az.ID, &az.Region, &az.Name, &az.NSPAddr, &az.Status, &lastHeartbeat,
	)

	if err != nil {
		return nil, fmt.Errorf("AZ不存在: %s/%s", region, azID)
	}

	az.LastHeartbeat = lastHeartbeat
	return az, nil
}

// GetRegionAZs 获取Region下的所有AZ
func (r *Registry) GetRegionAZs(ctx context.Context, region string) ([]*models.AZ, error) {
	query := `SELECT az_id, region, az_name, nsp_addr, status, UNIX_TIMESTAMP(last_heartbeat)
	          FROM az_registry WHERE region = ? AND status = 'online'`

	rows, err := db.GetDB().Query(query, region)
	if err != nil {
		return nil, fmt.Errorf("获取Region的AZ列表失败: %v", err)
	}
	defer rows.Close()

	azs := make([]*models.AZ, 0)
	for rows.Next() {
		az := &models.AZ{}
		var lastHeartbeat int64
		err := rows.Scan(&az.ID, &az.Region, &az.Name, &az.NSPAddr, &az.Status, &lastHeartbeat)
		if err != nil {
			continue
		}
		az.LastHeartbeat = lastHeartbeat
		azs = append(azs, az)
	}

	if len(azs) == 0 {
		return nil, fmt.Errorf("Region %s 没有注册的AZ", region)
	}

	return azs, nil
}

// Heartbeat AZ心跳更新
func (r *Registry) Heartbeat(ctx context.Context, region, azID string) error {
	query := `UPDATE az_registry SET last_heartbeat = NOW(), status = 'online' 
	          WHERE region = ? AND az_id = ?`

	result, err := db.GetDB().Exec(query, region, azID)
	if err != nil {
		return fmt.Errorf("更新心跳失败: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("AZ不存在: %s/%s", region, azID)
	}

	return nil
}

// CheckAZHealth 检查AZ健康状态
func (r *Registry) CheckAZHealth(ctx context.Context, region, azID string) (bool, error) {
	az, err := r.GetAZ(ctx, region, azID)
	if err != nil {
		return false, err
	}

	// 如果超过5分钟没有心跳，认为不健康
	if time.Now().Unix()-az.LastHeartbeat > 300 {
		// 更新状态为offline
		db.GetDB().Exec(`UPDATE az_registry SET status = 'offline' WHERE region = ? AND az_id = ?`, region, azID)
		return false, nil
	}

	return az.Status == "online", nil
}

// ListAllRegions 列出所有Region
func (r *Registry) ListAllRegions(ctx context.Context) ([]string, error) {
	query := `SELECT DISTINCT region FROM az_registry WHERE status = 'online'`

	rows, err := db.GetDB().Query(query)
	if err != nil {
		return nil, fmt.Errorf("获取Region列表失败: %v", err)
	}
	defer rows.Close()

	regions := make([]string, 0)
	for rows.Next() {
		var region string
		if err := rows.Scan(&region); err != nil {
			continue
		}
		regions = append(regions, region)
	}

	return regions, nil
}
