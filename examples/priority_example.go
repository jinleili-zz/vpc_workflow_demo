package examples

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/RichardKnop/machinery/v1"
	machineryTasks "github.com/RichardKnop/machinery/v1/tasks"
	"github.com/google/uuid"
)

// VPCRequest VPC创建请求
type VPCRequest struct {
	VPCName      string `json:"vpc_name"`
	VPCID        string `json:"vpc_id"`
	VRFName      string `json:"vrf_name"`
	VLANId       int    `json:"vlan_id"`
	FirewallZone string `json:"firewall_zone"`
}

// PriorityTaskExamples 任务优先级示例
type PriorityTaskExamples struct {
	server *machinery.Server
}

// NewPriorityTaskExamples 创建优先级任务示例
func NewPriorityTaskExamples(server *machinery.Server) *PriorityTaskExamples {
	return &PriorityTaskExamples{server: server}
}

// Example1_WithPriority 使用优先级的任务示例
// 高优先级任务会优先执行
func (p *PriorityTaskExamples) Example1_WithPriority() error {
	log.Println("=== 任务优先级示例 ===")

	// 创建两个VPC请求
	highPriorityReq := VPCRequest{
		VPCName:      "high-priority-vpc",
		VPCID:        uuid.New().String(),
		VRFName:      "VRF-HIGH",
		VLANId:       100,
		FirewallZone: "high-zone",
	}

	lowPriorityReq := VPCRequest{
		VPCName:      "low-priority-vpc",
		VPCID:        uuid.New().String(),
		VRFName:      "VRF-LOW",
		VLANId:       200,
		FirewallZone: "low-zone",
	}

	highReqJSON, _ := json.Marshal(highPriorityReq)
	lowReqJSON, _ := json.Marshal(lowPriorityReq)

	// 创建低优先级任务 (Priority = 1)
	lowPriorityTask := &machineryTasks.Signature{
		Name:     "create_vrf_on_switch",
		Priority: 1, // 低优先级
		Args: []machineryTasks.Arg{
			{Type: "string", Value: string(lowReqJSON)},
		},
	}

	// 创建高优先级任务 (Priority = 9)
	highPriorityTask := &machineryTasks.Signature{
		Name:     "create_vrf_on_switch",
		Priority: 9, // 高优先级 (0-9, 9为最高)
		Args: []machineryTasks.Arg{
			{Type: "string", Value: string(highReqJSON)},
		},
	}

	// 先发送低优先级任务
	_, err := p.server.SendTask(lowPriorityTask)
	if err != nil {
		return fmt.Errorf("发送低优先级任务失败: %v", err)
	}
	fmt.Println("✓ 已发送低优先级任务")

	// 后发送高优先级任务
	_, err = p.server.SendTask(highPriorityTask)
	if err != nil {
		return fmt.Errorf("发送高优先级任务失败: %v", err)
	}
	fmt.Println("✓ 已发送高优先级任务")

	fmt.Println("\n💡 说明:")
	fmt.Println("  - 优先级范围: 0-9 (9为最高优先级)")
	fmt.Println("  - 高优先级任务会优先执行")
	fmt.Println("  - 优先级相同的任务按FIFO顺序执行")

	return nil
}

// Example2_PriorityInChain 任务链中的优先级示例
func (p *PriorityTaskExamples) Example2_PriorityInChain() error {
	log.Println("=== 任务链中的优先级示例 ===")

	vpcRequest := VPCRequest{
		VPCName:      "chain-priority-vpc",
		VPCID:        uuid.New().String(),
		VRFName:      "VRF-CHAIN",
		VLANId:       300,
		FirewallZone: "chain-zone",
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 在任务链中设置优先级
	task1 := &machineryTasks.Signature{
		Name:     "create_vrf_on_switch",
		Priority: 5, // 中等优先级
		Args: []machineryTasks.Arg{
			{Type: "string", Value: string(requestJSON)},
		},
	}

	task2 := &machineryTasks.Signature{
		Name:     "create_vlan_subinterface",
		Priority: 5, // 中等优先级
		Args: []machineryTasks.Arg{
			{Type: "string", Value: string(requestJSON)},
		},
	}

	task3 := &machineryTasks.Signature{
		Name:     "create_firewall_zone",
		Priority: 5, // 中等优先级
		Args: []machineryTasks.Arg{
			{Type: "string", Value: string(requestJSON)},
		},
	}

	// 创建任务链
	chain, err := machineryTasks.NewChain(task1, task2, task3)
	if err != nil {
		return fmt.Errorf("创建任务链失败: %v", err)
	}

	_, err = p.server.SendChain(chain)
	if err != nil {
		return fmt.Errorf("发送任务链失败: %v", err)
	}

	fmt.Println("✓ 已发送带优先级的任务链")
	fmt.Println("  - 任务链中所有任务具有相同优先级")
	fmt.Println("  - 任务链内保持严格的顺序执行")

	return nil
}

// Example3_PriorityWithGroup 任务组中的优先级示例
func (p *PriorityTaskExamples) Example3_PriorityWithGroup() error {
	log.Println("=== 任务组中的优先级示例 ===")

	vrfReq := VPCRequest{
		VPCName:      "group-vrf",
		VPCID:        uuid.New().String(),
		VRFName:      "VRF-GROUP1",
		VLANId:       400,
		FirewallZone: "group-zone1",
	}

	vlanReq := VPCRequest{
		VPCName:      "group-vlan",
		VPCID:        uuid.New().String(),
		VRFName:      "VRF-GROUP2",
		VLANId:       500,
		FirewallZone: "group-zone2",
	}

	vrfReqJSON, _ := json.Marshal(vrfReq)
	vlanReqJSON, _ := json.Marshal(vlanReq)

	// 创建不同优先级的任务
	highPriorityTask := &machineryTasks.Signature{
		Name:     "create_vrf_on_switch",
		Priority: 8, // 高优先级
		Args: []machineryTasks.Arg{
			{Type: "string", Value: string(vrfReqJSON)},
		},
	}

	lowPriorityTask := &machineryTasks.Signature{
		Name:     "create_vlan_subinterface",
		Priority: 2, // 低优先级
		Args: []machineryTasks.Arg{
			{Type: "string", Value: string(vlanReqJSON)},
		},
	}

	// 创建任务组
	group := machineryTasks.NewGroup(highPriorityTask, lowPriorityTask)

	// 发送任务组
	_, err := p.server.SendGroup(group, 0)
	if err != nil {
		return fmt.Errorf("发送任务组失败: %v", err)
	}

	fmt.Println("✓ 已发送带优先级的任务组")
	fmt.Println("  - 高优先级任务会优先执行")
	fmt.Println("  - 任务组中的任务并行执行")

	return nil
}

/*
================================================================================
go-machinery 任务优先级说明
================================================================================

优先级字段:
- 类型: uint8
- 范围: 0-9 (9为最高优先级)
- 默认值: 0

使用场景:
1. 紧急任务优先执行
2. 重要客户任务优先处理
3. 系统维护任务优先执行

注意事项:
1. 优先级仅影响同一队列中的任务调度顺序
2. 不同队列的任务优先级无法直接比较
3. 优先级高的任务会抢占优先级低的任务执行资源
4. 任务链内的顺序执行不受优先级影响

================================================================================
*/
