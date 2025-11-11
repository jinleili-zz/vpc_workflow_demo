package rocketmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/go-redis/redis/v8"
)

// Config RocketMQ配置
type Config struct {
	NameServerAddrs []string
	GroupName       string
	InstanceName    string
	RetryTimes      int
}

// Producer RocketMQ生产者封装
type Producer struct {
	producer rocketmq.Producer
	redis    *redis.Client
}

// NewProducer 创建生产者
func NewProducer(cfg *Config, redisClient *redis.Client) (*Producer, error) {
	// 确保NameServer地址格式正确
	if len(cfg.NameServerAddrs) == 0 {
		return nil, fmt.Errorf("NameServer地址列表为空")
	}

	log.Printf("[RocketMQ Producer] 正在创建 Producer, NameServers: %v", cfg.NameServerAddrs)
	log.Printf("[RocketMQ Producer] Group: %s, Instance: %s", cfg.GroupName, cfg.InstanceName)

	// 使用WithNameServer API（更稳定）
	p, err := rocketmq.NewProducer(
		producer.WithNameServer(cfg.NameServerAddrs),
		producer.WithRetry(cfg.RetryTimes),
		producer.WithGroupName(cfg.GroupName),
		producer.WithInstanceName(cfg.InstanceName),
	)
	if err != nil {
		log.Printf("[RocketMQ Producer] 创建 Producer 失败: %v", err)
		return nil, fmt.Errorf("创建RocketMQ生产者失败: %v", err)
	}

	log.Printf("[RocketMQ Producer] Producer 创建成功，正在启动...")
	err = p.Start()
	if err != nil {
		log.Printf("[RocketMQ Producer] 启动 Producer 失败: %v", err)
		return nil, fmt.Errorf("启动RocketMQ生产者失败: %v", err)
	}

	log.Printf("[RocketMQ Producer] Producer 启动成功")
	return &Producer{
		producer: p,
		redis:    redisClient,
	}, nil
}

// TaskMessage 任务消息
type TaskMessage struct {
	TaskID    string                 `json:"task_id"`
	TaskName  string                 `json:"task_name"`
	Args      []interface{}          `json:"args"`
	Priority  int                    `json:"priority"`
	ChainInfo *ChainInfo             `json:"chain_info,omitempty"`
	GroupInfo *GroupInfo             `json:"group_info,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ChainInfo 链式任务信息
type ChainInfo struct {
	ChainID    string `json:"chain_id"`
	TaskIndex  int    `json:"task_index"`
	TotalTasks int    `json:"total_tasks"`
	NextTask   string `json:"next_task,omitempty"`
	IsLastTask bool   `json:"is_last_task"`
}

// GroupInfo 组任务信息
type GroupInfo struct {
	GroupID    string `json:"group_id"`
	TotalTasks int    `json:"total_tasks"`
}

// SendTask 发送单个任务
func (p *Producer) SendTask(topic string, msg *TaskMessage) (string, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("序列化任务消息失败: %v", err)
	}

	message := primitive.NewMessage(topic, body)

	// 设置优先级
	if msg.Priority > 0 {
		message.WithProperty("PRIORITY", fmt.Sprintf("%d", msg.Priority))
	}

	res, err := p.producer.SendSync(context.Background(), message)
	if err != nil {
		return "", fmt.Errorf("发送任务失败: %v", err)
	}

	// 存储任务状态到Redis
	p.saveTaskState(msg.TaskID, "pending", nil)

	log.Printf("[RocketMQ Producer] 发送任务成功: TaskID=%s, MsgID=%s", msg.TaskID, res.MsgID)
	return res.MsgID, nil
}

// SendChain 发送链式任务
func (p *Producer) SendChain(topic string, chainID string, tasks []*TaskMessage) error {
	if len(tasks) == 0 {
		return fmt.Errorf("任务链为空")
	}

	// 为每个任务添加链信息
	for i, task := range tasks {
		task.ChainInfo = &ChainInfo{
			ChainID:    chainID,
			TaskIndex:  i,
			TotalTasks: len(tasks),
			IsLastTask: i == len(tasks)-1,
		}
		if i < len(tasks)-1 {
			task.ChainInfo.NextTask = tasks[i+1].TaskName
		}
	}

	// 只发送第一个任务，后续任务由Worker完成后触发
	_, err := p.SendTask(topic, tasks[0])
	if err != nil {
		return err
	}

	// 存储完整链信息到Redis
	p.saveChainInfo(chainID, tasks)

	log.Printf("[RocketMQ Producer] 发送任务链成功: ChainID=%s, 任务数=%d", chainID, len(tasks))
	return nil
}

// SendGroup 发送组任务(并行执行)
func (p *Producer) SendGroup(topic string, groupID string, tasks []*TaskMessage) error {
	if len(tasks) == 0 {
		return fmt.Errorf("任务组为空")
	}

	// 为每个任务添加组信息
	for _, task := range tasks {
		task.GroupInfo = &GroupInfo{
			GroupID:    groupID,
			TotalTasks: len(tasks),
		}
	}

	// 并行发送所有任务
	var wg sync.WaitGroup
	errChan := make(chan error, len(tasks))

	for _, task := range tasks {
		wg.Add(1)
		go func(t *TaskMessage) {
			defer wg.Done()
			_, err := p.SendTask(topic, t)
			if err != nil {
				errChan <- err
			}
		}(task)
	}

	wg.Wait()
	close(errChan)

	// 检查是否有错误
	if len(errChan) > 0 {
		return <-errChan
	}

	log.Printf("[RocketMQ Producer] 发送任务组成功: GroupID=%s, 任务数=%d", groupID, len(tasks))
	return nil
}

// saveTaskState 保存任务状态到Redis
func (p *Producer) saveTaskState(taskID string, state string, result interface{}) {
	ctx := context.Background()
	key := fmt.Sprintf("task_state:%s", taskID)

	stateData := map[string]interface{}{
		"state":      state,
		"updated_at": time.Now().Unix(),
	}
	if result != nil {
		stateData["result"] = result
	}

	data, _ := json.Marshal(stateData)
	p.redis.Set(ctx, key, data, 24*time.Hour)
}

// saveChainInfo 保存链信息到Redis
func (p *Producer) saveChainInfo(chainID string, tasks []*TaskMessage) {
	ctx := context.Background()
	key := fmt.Sprintf("chain_info:%s", chainID)

	data, _ := json.Marshal(tasks)
	p.redis.Set(ctx, key, data, 24*time.Hour)
}

// GetTaskState 获取任务状态
func (p *Producer) GetTaskState(taskID string) (string, error) {
	ctx := context.Background()
	key := fmt.Sprintf("task_state:%s", taskID)

	data, err := p.redis.Get(ctx, key).Result()
	if err != nil {
		return "", err
	}

	var stateData map[string]interface{}
	if err := json.Unmarshal([]byte(data), &stateData); err != nil {
		return "", err
	}

	return stateData["state"].(string), nil
}

// Close 关闭生产者
func (p *Producer) Close() error {
	return p.producer.Shutdown()
}

// Consumer RocketMQ消费者封装
type Consumer struct {
	consumer rocketmq.PushConsumer
	redis    *redis.Client
	handlers map[string]TaskHandler
	producer *Producer
	topic    string
	mu       sync.RWMutex
}

// TaskHandler 任务处理函数
type TaskHandler func(args ...interface{}) (interface{}, error)

// NewConsumer 创建消费者
func NewConsumer(cfg *Config, topic string, redisClient *redis.Client, prod *Producer) (*Consumer, error) {
	// 确保NameServer地址格式正确
	if len(cfg.NameServerAddrs) == 0 {
		return nil, fmt.Errorf("NameServer地址列表为空")
	}

	// 使用WithNameServer API（更稳定）
	c, err := rocketmq.NewPushConsumer(
		consumer.WithNameServer(cfg.NameServerAddrs),
		consumer.WithGroupName(cfg.GroupName),
		consumer.WithConsumerModel(consumer.Clustering),
		consumer.WithConsumeFromWhere(consumer.ConsumeFromFirstOffset),
	)
	if err != nil {
		return nil, fmt.Errorf("创建RocketMQ消费者失败: %v", err)
	}

	cons := &Consumer{
		consumer: c,
		redis:    redisClient,
		handlers: make(map[string]TaskHandler),
		producer: prod,
		topic:    topic,
	}

	// 订阅Topic
	err = c.Subscribe(topic, consumer.MessageSelector{}, cons.handleMessage)
	if err != nil {
		return nil, fmt.Errorf("订阅Topic失败: %v", err)
	}

	return cons, nil
}

// RegisterTask 注册任务处理器
func (c *Consumer) RegisterTask(taskName string, handler TaskHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[taskName] = handler
}

// handleMessage 处理消息
func (c *Consumer) handleMessage(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	for _, msg := range msgs {
		var taskMsg TaskMessage
		if err := json.Unmarshal(msg.Body, &taskMsg); err != nil {
			log.Printf("[RocketMQ Consumer] 解析消息失败: %v", err)
			return consumer.ConsumeRetryLater, nil
		}

		log.Printf("[RocketMQ Consumer] 收到任务: TaskID=%s, TaskName=%s", taskMsg.TaskID, taskMsg.TaskName)

		// 查找处理器
		c.mu.RLock()
		handler, ok := c.handlers[taskMsg.TaskName]
		c.mu.RUnlock()

		if !ok {
			log.Printf("[RocketMQ Consumer] 未注册的任务类型: %s", taskMsg.TaskName)
			return consumer.ConsumeSuccess, nil
		}

		// 执行任务
		c.updateTaskState(taskMsg.TaskID, "running", nil)

		result, err := handler(taskMsg.Args...)
		if err != nil {
			log.Printf("[RocketMQ Consumer] 任务执行失败: TaskID=%s, Error=%v", taskMsg.TaskID, err)
			c.updateTaskState(taskMsg.TaskID, "failed", err.Error())
			return consumer.ConsumeRetryLater, nil
		}

		c.updateTaskState(taskMsg.TaskID, "success", result)
		log.Printf("[RocketMQ Consumer] 任务执行成功: TaskID=%s", taskMsg.TaskID)

		// 如果是链式任务，触发下一个任务
		if taskMsg.ChainInfo != nil && !taskMsg.ChainInfo.IsLastTask {
			c.triggerNextTaskInChain(&taskMsg, result)
		}

		// 如果是组任务，检查是否全部完成
		if taskMsg.GroupInfo != nil {
			c.checkGroupCompletion(taskMsg.GroupInfo.GroupID)
		}
	}

	return consumer.ConsumeSuccess, nil
}

// updateTaskState 更新任务状态
func (c *Consumer) updateTaskState(taskID string, state string, result interface{}) {
	ctx := context.Background()
	key := fmt.Sprintf("task_state:%s", taskID)

	stateData := map[string]interface{}{
		"state":      state,
		"updated_at": time.Now().Unix(),
	}
	if result != nil {
		stateData["result"] = result
	}

	data, _ := json.Marshal(stateData)
	c.redis.Set(ctx, key, data, 24*time.Hour)
}

// triggerNextTaskInChain 触发链中的下一个任务
func (c *Consumer) triggerNextTaskInChain(currentTask *TaskMessage, previousResult interface{}) {
	ctx := context.Background()
	chainKey := fmt.Sprintf("chain_info:%s", currentTask.ChainInfo.ChainID)

	// 从Redis获取完整链信息
	data, err := c.redis.Get(ctx, chainKey).Result()
	if err != nil {
		log.Printf("[RocketMQ Consumer] 获取链信息失败: %v", err)
		return
	}

	var tasks []*TaskMessage
	if err := json.Unmarshal([]byte(data), &tasks); err != nil {
		log.Printf("[RocketMQ Consumer] 解析链信息失败: %v", err)
		return
	}

	// 获取下一个任务
	nextIndex := currentTask.ChainInfo.TaskIndex + 1
	if nextIndex >= len(tasks) {
		log.Printf("[RocketMQ Consumer] 任务链已完成: ChainID=%s", currentTask.ChainInfo.ChainID)
		return
	}

	// 发送下一个任务
	nextTask := tasks[nextIndex]
	if _, err := c.producer.SendTask(c.topic, nextTask); err != nil {
		log.Printf("[RocketMQ Consumer] 触发下一个任务失败: %v", err)
	} else {
		log.Printf("[RocketMQ Consumer] 成功触发下一个任务: %s", nextTask.TaskName)
	}
}

// checkGroupCompletion 检查组任务完成情况
func (c *Consumer) checkGroupCompletion(groupID string) {
	// TODO: 实现组任务完成检查逻辑
	log.Printf("[RocketMQ Consumer] 检查组任务完成状态: GroupID=%s", groupID)
}

// Start 启动消费者
func (c *Consumer) Start() error {
	return c.consumer.Start()
}

// Close 关闭消费者
func (c *Consumer) Close() error {
	return c.consumer.Shutdown()
}
