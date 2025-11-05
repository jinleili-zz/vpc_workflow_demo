package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"workflow_qoder/internal/models"

	"github.com/go-redis/redis/v8"
)

// Registry AZ注册中心
type Registry struct {
	redisClient *redis.Client
}

// NewRegistry 创建注册中心
func NewRegistry(redisClient *redis.Client) *Registry {
	return &Registry{
		redisClient: redisClient,
	}
}

// RegisterAZ 注册AZ
func (r *Registry) RegisterAZ(ctx context.Context, az *models.AZ) error {
	key := fmt.Sprintf("az:%s:%s", az.Region, az.ID)

	// 设置当前时间为心跳时间
	az.LastHeartbeat = time.Now().Unix()
	az.Status = "online"

	data, err := json.Marshal(az)
	if err != nil {
		return fmt.Errorf("序列化AZ信息失败: %v", err)
	}

	// 存储AZ信息（24小时过期，需要定期心跳续期）
	err = r.redisClient.Set(ctx, key, data, 24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("存储AZ信息失败: %v", err)
	}

	// 将AZ添加到Region的AZ列表中
	regionKey := fmt.Sprintf("region:%s:azs", az.Region)
	err = r.redisClient.SAdd(ctx, regionKey, az.ID).Err()
	if err != nil {
		return fmt.Errorf("添加AZ到Region列表失败: %v", err)
	}

	return nil
}

// GetAZ 获取AZ信息
func (r *Registry) GetAZ(ctx context.Context, region, azID string) (*models.AZ, error) {
	key := fmt.Sprintf("az:%s:%s", region, azID)

	data, err := r.redisClient.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("AZ不存在: %s/%s", region, azID)
	}
	if err != nil {
		return nil, fmt.Errorf("获取AZ信息失败: %v", err)
	}

	var az models.AZ
	err = json.Unmarshal([]byte(data), &az)
	if err != nil {
		return nil, fmt.Errorf("解析AZ信息失败: %v", err)
	}

	return &az, nil
}

// GetRegionAZs 获取Region下的所有AZ
func (r *Registry) GetRegionAZs(ctx context.Context, region string) ([]*models.AZ, error) {
	// 获取AZ ID列表
	regionKey := fmt.Sprintf("region:%s:azs", region)
	azIDs, err := r.redisClient.SMembers(ctx, regionKey).Result()
	if err != nil {
		return nil, fmt.Errorf("获取Region的AZ列表失败: %v", err)
	}

	if len(azIDs) == 0 {
		return nil, fmt.Errorf("Region %s 没有注册的AZ", region)
	}

	// 获取每个AZ的详细信息
	azs := make([]*models.AZ, 0, len(azIDs))
	for _, azID := range azIDs {
		az, err := r.GetAZ(ctx, region, azID)
		if err != nil {
			// 跳过获取失败的AZ
			continue
		}
		azs = append(azs, az)
	}

	return azs, nil
}

// Heartbeat AZ心跳更新
func (r *Registry) Heartbeat(ctx context.Context, region, azID string) error {
	az, err := r.GetAZ(ctx, region, azID)
	if err != nil {
		return err
	}

	// 更新心跳时间
	az.LastHeartbeat = time.Now().Unix()
	az.Status = "online"

	key := fmt.Sprintf("az:%s:%s", region, azID)
	data, err := json.Marshal(az)
	if err != nil {
		return fmt.Errorf("序列化AZ信息失败: %v", err)
	}

	// 续期24小时
	err = r.redisClient.Set(ctx, key, data, 24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("更新心跳失败: %v", err)
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
		return false, nil
	}

	return az.Status == "online", nil
}

// ListAllRegions 列出所有Region
func (r *Registry) ListAllRegions(ctx context.Context) ([]string, error) {
	// 通过pattern匹配获取所有region的key
	keys, err := r.redisClient.Keys(ctx, "region:*:azs").Result()
	if err != nil {
		return nil, fmt.Errorf("获取Region列表失败: %v", err)
	}

	regions := make([]string, 0, len(keys))
	for _, key := range keys {
		// 从 "region:cn-beijing:azs" 提取 "cn-beijing"
		region := key[7 : len(key)-4]
		regions = append(regions, region)
	}

	return regions, nil
}
