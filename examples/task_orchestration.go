package examples

import (
	"encoding/json"
	"fmt"

	"workflow_qoder/tasks"

	"github.com/RichardKnop/machinery/v1"
	machineryTasks "github.com/RichardKnop/machinery/v1/tasks"
)

// VPCRequest VPC创建请求
type VPCRequest struct {
	VPCName      string `json:"vpc_name"`
	VPCID        string `json:"vpc_id"`
	VRFName      string `json:"vrf_name"`
	VLANId       int    `json:"vlan_id"`
	FirewallZone string `json:"firewall_zone"`
}

// TaskOrchestrationExamples 任务编排示例
type TaskOrchestrationExamples struct {
	server *machinery.Server
}

func NewTaskOrchestrationExamples(server *machinery.Server) *TaskOrchestrationExamples {
	return &TaskOrchestrationExamples{server: server}
}

// Example1_IndependentTasks 示例1: 独立任务（当前使用的方式）
// 三个任务独立执行，没有顺序保证，适合互不依赖的任务
func (t *TaskOrchestrationExamples) Example1_IndependentTasks(req VPCRequest) error {
	requestJSON, _ := json.Marshal(req)

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

	// 发送三个独立任务
	t.server.SendTask(task1)
	t.server.SendTask(task2)
	t.server.SendTask(task3)

	fmt.Println("✓ 发送了3个独立任务，并行执行，无顺序保证")
	return nil
}

// Example2_ChainTasks 示例2: 任务链（严格顺序执行）
// VRF -> VLAN -> Firewall 按顺序执行
// 注意：需要任务函数支持接收前一个任务的结果
func (t *TaskOrchestrationExamples) Example2_ChainTasks(req VPCRequest) error {
	requestJSON, _ := json.Marshal(req)

	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		// Args 留空，会接收 task1 的返回值
	}
	task3 := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		// Args 留空，会接收 task2 的返回值
	}

	// 创建任务链
	chain, err := machineryTasks.NewChain(task1, task2, task3)
	if err != nil {
		return err
	}

	_, err = t.server.SendChain(chain)
	fmt.Println("✓ 发送了任务链，严格按顺序执行: VRF -> VLAN -> Firewall")
	return err
}

// Example3_GroupTasks 示例3: 任务组（并行执行）
// VRF 和 VLAN 并行创建，提高效率
func (t *TaskOrchestrationExamples) Example3_GroupTasks(req VPCRequest) error {
	requestJSON, _ := json.Marshal(req)

	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 创建任务组
	group := machineryTasks.NewGroup(task1, task2)
	
	_, err := t.server.SendGroup(group, 0) // 0 表示不限制并发数
	fmt.Println("✓ 发送了任务组，VRF 和 VLAN 并行执行")
	return err
}

// Example4_ChordTasks 示例4: Chord（先并行后汇总）
// VRF 和 VLAN 并行执行完成后，再执行防火墙配置
func (t *TaskOrchestrationExamples) Example4_ChordTasks(req VPCRequest) error {
	requestJSON, _ := json.Marshal(req)

	// 并行任务
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 回调任务（在所有并行任务完成后执行）
	callback := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 创建 Chord
	group := machineryTasks.NewGroup(task1, task2)
	chord, err := machineryTasks.NewChord(group, callback)
	if err != nil {
		return err
	}

	_, err = t.server.SendChord(chord, 0)
	fmt.Println("✓ 发送了 Chord，(VRF || VLAN) -> Firewall")
	return err
}

// Example5_ComplexOrchestration 示例5: 复杂编排
// 场景：先创建VRF，然后并行创建VLAN和配置防火墙
// VRF -> (VLAN || Firewall)
func (t *TaskOrchestrationExamples) Example5_ComplexOrchestration(req VPCRequest) error {
	requestJSON, _ := json.Marshal(req)

	// 第一步：创建VRF
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 第二步：并行创建VLAN和防火墙
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task3 := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 将task2和task3组成一个组
	group := machineryTasks.NewGroup(task2, task3)

	// task1执行完后，触发group
	task1.OnSuccess = []*machineryTasks.Signature{
		{
			Name: "create_vlan_subinterface",
			Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
		},
		{
			Name: "create_firewall_zone",
			Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
		},
	}

	// 发送第一个任务（会自动触发后续任务）
	_, err := t.server.SendTask(task1)
	fmt.Println("✓ 发送了复杂编排，VRF -> (VLAN || Firewall)")
	return err
}

// Example6_WithCallback 示例6: 带回调的任务
// 每个任务成功后执行回调（如记录日志、更新状态）
func (t *TaskOrchestrationExamples) Example6_WithCallback(req VPCRequest) error {
	requestJSON, _ := json.Marshal(req)

	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
		// 成功后的回调
		OnSuccess: []*machineryTasks.Signature{
			{
				Name: "log_task_success",
				Args: []machineryTasks.Arg{
					{Type: "string", Value: "VRF创建成功"},
				},
			},
		},
		// 失败后的回调
		OnError: []*machineryTasks.Signature{
			{
				Name: "log_task_error",
				Args: []machineryTasks.Arg{
					{Type: "string", Value: "VRF创建失败"},
				},
			},
		},
	}

	_, err := t.server.SendTask(task1)
	fmt.Println("✓ 发送了带回调的任务")
	return err
}

/*
================================================================================
任务编排对比总结
================================================================================

方式              | 执行顺序          | 结果传递 | 适用场景
----------------|-----------------|---------|---------------------------
独立任务 (SendTask) | 无序，并行        | 否      | 互不依赖的任务
任务链 (Chain)     | 严格顺序，串行    | 是      | 有严格依赖关系的流程
任务组 (Group)     | 并行             | 否      | 可以并行的独立任务
Chord            | 先并行后汇总      | 是      | MapReduce模式
复杂编排          | 自定义           | 可选    | 复杂业务流程

================================================================================
在 VPC 场景中的选择建议
================================================================================

1. 如果 VRF、VLAN、Firewall 互不依赖
   → 使用 Example1 (独立任务) 或 Example3 (任务组)

2. 如果必须按顺序: VRF -> VLAN -> Firewall
   → 使用 Example2 (任务链)
   注意：需要修改任务函数签名以接收前一个任务的结果

3. 如果 VRF 必须先创建，VLAN 和 Firewall 可以并行
   → 使用 Example5 (复杂编排)

4. 如果 VRF 和 VLAN 并行创建完成后，再配置 Firewall
   → 使用 Example4 (Chord)

================================================================================
*/
