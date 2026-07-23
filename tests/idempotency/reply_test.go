package idempotency

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/taskqueue/asynqbroker"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/db/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/orchestration"
	"workflow_qoder/internal/queue"
)

// 使用独立的 region/az 与 Redis broker DB，避免与业务测试环境的服务/worker 互相消费。
const (
	replyTestRegion = "idem-unit"
	replyTestAZ     = "ut-az1"
	replyTestBrokerDB = 2
)

func newReplyTestManager(t *testing.T) (*orchestration.Manager, *dao.VPCDAO, *dao.TaskDAO) {
	t.Helper()
	requirePostgres(t)

	db := openDB(t, azDBName)
	redisOpt := config.MakeAsynqRedisOpt(redisAddr, replyTestBrokerDB)
	broker := asynqbroker.NewBroker(redisOpt)
	t.Cleanup(func() { broker.Close() })

	taskDAO := dao.NewTaskDAO(db)
	vpcDAO := dao.NewVPCDAO(db)

	mgr := orchestration.NewManager(
		broker,
		taskDAO,
		func(deviceType string, priority int) string {
			return queue.GetPriorityQueueName(replyTestRegion, replyTestAZ, queue.DeviceType(deviceType), queue.TaskPriority(priority))
		},
		queue.GetReplyQueueName(replyTestRegion, replyTestAZ, "vpc"),
	)
	mgr.RegisterResourceStore(models.ResourceTypeVPC, vpcDAO)
	return mgr, vpcDAO, taskDAO
}

func buildReplyTask(t *testing.T, resourceID string, stepIndex, totalSteps int, status orchestration.ReplyStatus) *taskqueue.Task {
	t.Helper()
	payload, err := json.Marshal(orchestration.ReplyPayload{
		TaskType: "create_vrf_on_switch",
		Status:   status,
		Result:   json.RawMessage(`{"ok":true}`),
		Error:    "",
	})
	if err != nil {
		t.Fatalf("序列化 reply 失败: %v", err)
	}
	return &taskqueue.Task{
		Payload: payload,
		Metadata: map[string]string{
			orchestration.MetadataKeyResourceID:   resourceID,
			orchestration.MetadataKeyResourceType: string(models.ResourceTypeVPC),
			orchestration.MetadataKeyStepIndex:    fmt.Sprintf("%d", stepIndex),
			orchestration.MetadataKeyTotalSteps:   fmt.Sprintf("%d", totalSteps),
		},
	}
}

// 重复/并发 Reply 只推进一次工作流（设计文档 7.8 节、15.2 节"两个 AZ Consumer 并发处理同 Reply"）。
// 不变量：completed_tasks + failed_tasks <= total_tasks；同一步骤只发布一次下一步。
func TestConcurrentDuplicateReply(t *testing.T) {
	mgr, vpcDAO, taskDAO := newReplyTestManager(t)
	ctx := context.Background()

	vpcID := uuid.NewString()
	vpcName := uniqueName("idem-reply-vpc")
	if err := vpcDAO.Create(ctx, &models.VPCResource{
		ID: vpcID, VPCName: vpcName, Region: replyTestRegion, AZ: replyTestAZ,
		VRFName: "vrf-" + vpcName, VLANId: 100, FirewallZone: "zone-" + vpcName,
		Status: models.ResourceStatusPending,
	}); err != nil {
		t.Fatalf("创建 VPC 资源失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = vpcDAO.GetByID(context.Background(), vpcID) // 保留现场便于排查；仅删除任务
		_ = taskDAO.DeleteByResourceID(context.Background(), vpcID)
	})

	steps := []orchestration.WorkflowStep{
		{TaskType: "create_vrf_on_switch", TaskName: "创建VRF", DeviceType: string(queue.DeviceTypeSwitch), Priority: 3, Payload: []byte(`{"vpc_name":"` + vpcName + `"}`)},
		{TaskType: "create_vlan_subinterface", TaskName: "创建VLAN子接口", DeviceType: string(queue.DeviceTypeSwitch), Priority: 3, Payload: []byte(`{"vpc_name":"` + vpcName + `"}`)},
		{TaskType: "create_firewall_zone", TaskName: "创建防火墙安全区域", DeviceType: string(queue.DeviceTypeFirewall), Priority: 3, Payload: []byte(`{"vpc_name":"` + vpcName + `"}`)},
	}
	if _, err := mgr.SubmitWorkflow(ctx, orchestration.WorkflowDef{
		ResourceType: models.ResourceTypeVPC,
		ResourceID:   vpcID,
		AZ:           replyTestAZ,
		Steps:        steps,
	}); err != nil {
		t.Fatalf("提交工作流失败: %v", err)
	}

	// 1) 并发 5 个相同的 step-0 成功 Reply：只能有一个完成 CAS 推进
	reply := buildReplyTask(t, vpcID, 0, 3, orchestration.ReplyStatusSuccess)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- mgr.HandleReply(ctx, reply)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("HandleReply 出错: %v", err)
		}
	}

	vpc, err := vpcDAO.GetByID(ctx, vpcID)
	if err != nil {
		t.Fatalf("查询 VPC 失败: %v", err)
	}
	if vpc.CompletedTasks != 1 {
		t.Fatalf("并发重复 Reply 后 completed_tasks=%d, want 1", vpc.CompletedTasks)
	}
	if vpc.FailedTasks != 0 {
		t.Fatalf("failed_tasks=%d, want 0", vpc.FailedTasks)
	}

	tasks, err := taskDAO.GetByResourceID(ctx, vpcID)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("任务数=%d, want 3", len(tasks))
	}
	if tasks[0].Status != models.TaskStatusCompleted {
		t.Fatalf("task1 状态=%s, want completed", tasks[0].Status)
	}
	if tasks[1].Status != models.TaskStatusQueued || tasks[1].AsynqTaskID == "" {
		t.Fatalf("task2 状态=%s asynq_id=%s, want queued 且已发布", tasks[1].Status, tasks[1].AsynqTaskID)
	}

	// 2) 串行重复同一 Reply：计数不变
	if err := mgr.HandleReply(ctx, reply); err != nil {
		t.Fatalf("重复 HandleReply 出错: %v", err)
	}
	vpc, _ = vpcDAO.GetByID(ctx, vpcID)
	if vpc.CompletedTasks != 1 {
		t.Fatalf("串行重复 Reply 后 completed_tasks=%d, want 1", vpc.CompletedTasks)
	}

	// 3) 推进剩余步骤
	if err := mgr.HandleReply(ctx, buildReplyTask(t, vpcID, 1, 3, orchestration.ReplyStatusSuccess)); err != nil {
		t.Fatalf("step1 reply 失败: %v", err)
	}
	if err := mgr.HandleReply(ctx, buildReplyTask(t, vpcID, 2, 3, orchestration.ReplyStatusSuccess)); err != nil {
		t.Fatalf("step2 reply 失败: %v", err)
	}

	vpc, _ = vpcDAO.GetByID(ctx, vpcID)
	if vpc.CompletedTasks != 3 || vpc.Status != models.ResourceStatusRunning {
		t.Fatalf("完成后 completed=%d status=%s, want 3/running", vpc.CompletedTasks, vpc.Status)
	}

	// 4) 终态后重复 Reply（成功与失败）均不得改变状态与计数
	if err := mgr.HandleReply(ctx, buildReplyTask(t, vpcID, 2, 3, orchestration.ReplyStatusSuccess)); err != nil {
		t.Fatalf("终态重复 reply 失败: %v", err)
	}
	if err := mgr.HandleReply(ctx, buildReplyTask(t, vpcID, 2, 3, orchestration.ReplyStatusFailed)); err != nil {
		t.Fatalf("终态失败 reply 失败: %v", err)
	}
	vpc, _ = vpcDAO.GetByID(ctx, vpcID)
	if vpc.CompletedTasks != 3 || vpc.FailedTasks != 0 || vpc.Status != models.ResourceStatusRunning {
		t.Fatalf("终态后被污染: completed=%d failed=%d status=%s", vpc.CompletedTasks, vpc.FailedTasks, vpc.Status)
	}
}
