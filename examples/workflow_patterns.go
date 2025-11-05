package examples

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"workflow_qoder/tasks"

	"github.com/RichardKnop/machinery/v1"
	machineryTasks "github.com/RichardKnop/machinery/v1/tasks"
	"github.com/google/uuid"
)

// WorkflowPatterns 演示基于消息队列的workflow编排模式
type WorkflowPatterns struct {
	server *machinery.Server
}

// NewWorkflowPatterns 创建workflow模式示例
func NewWorkflowPatterns(server *machinery.Server) *WorkflowPatterns {
	return &WorkflowPatterns{server: server}
}

// Pattern1_Chain 模式1: Chain - 顺序执行
// 场景: VRF -> VLAN -> Firewall 必须按顺序执行
func (w *WorkflowPatterns) Pattern1_Chain(vpcName, vrfName string, vlanId int, firewallZone string) error {
	log.Println("=== Workflow模式1: Chain (顺序执行) ===")

	vpcRequest := tasks.VPCRequest{
		VPCName:      vpcName,
		VPCID:        uuid.New().String(),
		VRFName:      vrfName,
		VLANId:       vlanId,
		FirewallZone: firewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 创建任务链: task1 -> task2 -> task3
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task3 := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	chain, _ := machineryTasks.NewChain(task1, task2, task3)

	_, err := w.server.SendChain(chain)
	if err != nil {
		return fmt.Errorf("发送Chain失败: %v", err)
	}

	log.Println("✓ Chain workflow已发送: VRF -> VLAN -> Firewall")
	return nil
}

// Pattern2_Group 模式2: Group - 并行执行
// 场景: 所有任务可以同时执行，互不依赖
func (w *WorkflowPatterns) Pattern2_Group(vpcName, vrfName string, vlanId int, firewallZone string) error {
	log.Println("=== Workflow模式2: Group (并行执行) ===")

	vpcRequest := tasks.VPCRequest{
		VPCName:      vpcName,
		VPCID:        uuid.New().String(),
		VRFName:      vrfName,
		VLANId:       vlanId,
		FirewallZone: firewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 创建任务组: task1 || task2 || task3 (并行)
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task3 := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	group, _ := machineryTasks.NewGroup(task1, task2, task3)

	_, err := w.server.SendGroup(group, 0)
	if err != nil {
		return fmt.Errorf("发送Group失败: %v", err)
	}

	log.Println("✓ Group workflow已发送: VRF || VLAN || Firewall (并行)")
	return nil
}

// Pattern3_Chord 模式3: Chord - 先并行，后回调
// 场景: VRF和VLAN可以并行执行，都完成后再执行Firewall
func (w *WorkflowPatterns) Pattern3_Chord(vpcName, vrfName string, vlanId int, firewallZone string) error {
	log.Println("=== Workflow模式3: Chord (并行+回调) ===")

	vpcRequest := tasks.VPCRequest{
		VPCName:      vpcName,
		VPCID:        uuid.New().String(),
		VRFName:      vrfName,
		VLANId:       vlanId,
		FirewallZone: firewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 第一阶段: VRF 和 VLAN 并行执行
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 第二阶段: 防火墙配置（回调任务）
	callback := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 创建 Chord: (VRF || VLAN) -> Firewall
	group, _ := machineryTasks.NewGroup(task1, task2)
	chord, _ := machineryTasks.NewChord(group, callback)

	_, err := w.server.SendChord(chord, 0)
	if err != nil {
		return fmt.Errorf("发送Chord失败: %v", err)
	}

	log.Println("✓ Chord workflow已发送: (VRF || VLAN) -> Firewall")
	return nil
}

// Pattern4_ChainOfGroups 模式4: 混合模式 - Chain中包含Group
// 场景: 第一阶段并行，第二阶段顺序
func (w *WorkflowPatterns) Pattern4_ChainOfGroups(vpcName, vrfName string, vlanId int, firewallZone string) error {
	log.Println("=== Workflow模式4: Chain中包含Group (混合模式) ===")

	vpcRequest := tasks.VPCRequest{
		VPCName:      vpcName,
		VPCID:        uuid.New().String(),
		VRFName:      vrfName,
		VLANId:       vlanId,
		FirewallZone: firewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 第一组: VRF 和 VLAN 并行
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 第二个任务: Firewall
	task3 := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 创建组
	group1, _ := machineryTasks.NewGroup(task1, task2)

	// 使用Chord实现: Group -> Task
	chord, _ := machineryTasks.NewChord(group1, task3)

	_, err := w.server.SendChord(chord, 0)
	if err != nil {
		return fmt.Errorf("发送混合workflow失败: %v", err)
	}

	log.Println("✓ 混合workflow已发送: [VRF || VLAN] -> Firewall")
	return nil
}

// Pattern5_WithRetry 模式5: 带重试的任务
// 场景: 任务失败时自动重试
func (w *WorkflowPatterns) Pattern5_WithRetry(vpcName, vrfName string, vlanId int, firewallZone string) error {
	log.Println("=== Workflow模式5: 带重试机制 ===")

	vpcRequest := tasks.VPCRequest{
		VPCName:      vpcName,
		VPCID:        uuid.New().String(),
		VRFName:      vrfName,
		VLANId:       vlanId,
		FirewallZone: firewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 创建带重试配置的任务
	task := &machineryTasks.Signature{
		Name:         "create_vrf_on_switch",
		Args:         []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
		RetryCount:   3,  // 最多重试3次
		RetryTimeout: 60, // 重试超时时间60秒
	}

	_, err := w.server.SendTask(task)
	if err != nil {
		return fmt.Errorf("发送重试任务失败: %v", err)
	}

	log.Println("✓ 重试任务已发送: 失败时最多重试3次")
	return nil
}

// Pattern6_DelayedTask 模式6: 延迟任务
// 场景: 需要在特定时间执行的任务
func (w *WorkflowPatterns) Pattern6_DelayedTask(vpcName, vrfName string, vlanId int, firewallZone string, etaSeconds int64) error {
	log.Println("=== Workflow模式6: 延迟任务 ===")

	vpcRequest := tasks.VPCRequest{
		VPCName:      vpcName,
		VPCID:        uuid.New().String(),
		VRFName:      vrfName,
		VLANId:       vlanId,
		FirewallZone: firewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 创建延迟任务
	task := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
		// ETA: time.Now().Add(time.Duration(etaSeconds) * time.Second), // 设置执行时间
	}

	_, err := w.server.SendTask(task)
	if err != nil {
		return fmt.Errorf("发送延迟任务失败: %v", err)
	}

	log.Printf("✓ 延迟任务已发送: %d秒后执行\n", etaSeconds)
	return nil
}

// RunAllPatterns 运行所有workflow模式示例
func (w *WorkflowPatterns) RunAllPatterns() {
	log.Println("\n" + strings.Repeat("=", 60))
	log.Println("演示所有Workflow编排模式")
	log.Println(strings.Repeat("=", 60) + "\n")

	// 示例数据
	vpcName := "demo-vpc"
	vrfName := "VRF-100"
	vlanId := 100
	firewallZone := "zone-trust"

	// 执行各种模式
	w.Pattern1_Chain(vpcName, vrfName, vlanId, firewallZone)
	log.Println()

	w.Pattern2_Group(vpcName, vrfName, vlanId, firewallZone)
	log.Println()

	w.Pattern3_Chord(vpcName, vrfName, vlanId, firewallZone)
	log.Println()

	w.Pattern4_ChainOfGroups(vpcName, vrfName, vlanId, firewallZone)
	log.Println()

	w.Pattern5_WithRetry(vpcName, vrfName, vlanId, firewallZone)
	log.Println()

	w.Pattern6_DelayedTask(vpcName, vrfName, vlanId, firewallZone, 10)
	log.Println()

	log.Println(strings.Repeat("=", 60))
	log.Println("所有workflow模式已提交到消息队列")
	log.Println(strings.Repeat("=", 60))
}
