package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
)

type Producer struct {
	producer rocketmq.Producer
	namesrv  string
}

type Consumer struct {
	consumer rocketmq.PushConsumer
	namesrv  string
	group    string
	handlers map[string]MessageHandler
	mu       sync.RWMutex
}

type MessageHandler func(ctx context.Context, payload []byte) error

type TaskMessage struct {
	TaskType string          `json:"task_type"`
	Payload  json.RawMessage `json:"payload"`
}

func resolveNameserver(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return addr, nil
	}

	for _, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			return net.JoinHostPort(ipv4.String(), port), nil
		}
	}

	return addr, nil
}

func NewProducer(namesrvAddr string) (*Producer, error) {
	resolvedAddr, err := resolveNameserver(namesrvAddr)
	if err != nil {
		log.Printf("[RocketMQ] 解析NameServer地址失败: %v, 使用原地址", err)
		resolvedAddr = namesrvAddr
	}

	log.Printf("[RocketMQ] NameServer地址解析: %s -> %s", namesrvAddr, resolvedAddr)

	p, err := rocketmq.NewProducer(
		producer.WithNameServer([]string{resolvedAddr}),
		producer.WithRetry(3),
		producer.WithSendMsgTimeout(10*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("创建Producer失败: %v", err)
	}

	if err := p.Start(); err != nil {
		return nil, fmt.Errorf("启动Producer失败: %v", err)
	}

	log.Printf("[RocketMQ] Producer已启动, NameServer: %s", resolvedAddr)

	return &Producer{
		producer: p,
		namesrv:  resolvedAddr,
	}, nil
}

func (p *Producer) SendMessage(topic, taskType string, payload []byte) (string, error) {
	msg := TaskMessage{
		TaskType: taskType,
		Payload:  payload,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("序列化消息失败: %v", err)
	}

	rmqMsg := &primitive.Message{
		Topic: topic,
		Body:  msgBytes,
	}
	rmqMsg.WithTag(taskType)

	result, err := p.producer.SendSync(context.Background(), rmqMsg)
	if err != nil {
		return "", fmt.Errorf("发送消息失败: %v", err)
	}

	log.Printf("[RocketMQ] 消息已发送: topic=%s, taskType=%s, msgId=%s", topic, taskType, result.MsgID)
	return result.MsgID, nil
}

func (p *Producer) SendMessageWithDelay(topic, taskType string, payload []byte, delayLevel int) (string, error) {
	msg := TaskMessage{
		TaskType: taskType,
		Payload:  payload,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("序列化消息失败: %v", err)
	}

	rmqMsg := &primitive.Message{
		Topic: topic,
		Body:  msgBytes,
	}
	rmqMsg.WithTag(taskType)
	rmqMsg.WithDelayTimeLevel(delayLevel)

	result, err := p.producer.SendSync(context.Background(), rmqMsg)
	if err != nil {
		return "", fmt.Errorf("发送延迟消息失败: %v", err)
	}

	log.Printf("[RocketMQ] 延迟消息已发送: topic=%s, taskType=%s, msgId=%s, delayLevel=%d", topic, taskType, result.MsgID, delayLevel)
	return result.MsgID, nil
}

func (p *Producer) Close() error {
	if p.producer != nil {
		return p.producer.Shutdown()
	}
	return nil
}

func (p *Producer) EnsureTopicExists(topic string) error {
	testPayload := []byte(`{"test": true}`)
	rmqMsg := &primitive.Message{
		Topic: topic,
		Body:  testPayload,
	}
	rmqMsg.WithTag("__topic_init__")

	_, err := p.producer.SendSync(context.Background(), rmqMsg)
	if err != nil {
		return fmt.Errorf("创建topic失败: %v", err)
	}

	log.Printf("[RocketMQ] Topic已创建/确认存在: %s", topic)
	return nil
}

func NewConsumer(namesrvAddr, groupName string) (*Consumer, error) {
	resolvedAddr, err := resolveNameserver(namesrvAddr)
	if err != nil {
		log.Printf("[RocketMQ] 解析NameServer地址失败: %v, 使用原地址", err)
		resolvedAddr = namesrvAddr
	}

	log.Printf("[RocketMQ] Consumer NameServer地址解析: %s -> %s", namesrvAddr, resolvedAddr)

	c, err := rocketmq.NewPushConsumer(
		consumer.WithNameServer([]string{resolvedAddr}),
		consumer.WithGroupName(groupName),
		consumer.WithConsumerModel(consumer.Clustering),
		consumer.WithConsumeFromWhere(consumer.ConsumeFromLastOffset),
		consumer.WithConsumeMessageBatchMaxSize(1),
	)
	if err != nil {
		return nil, fmt.Errorf("创建Consumer失败: %v", err)
	}

	return &Consumer{
		consumer: c,
		namesrv:  resolvedAddr,
		group:    groupName,
		handlers: make(map[string]MessageHandler),
	}, nil
}

func (c *Consumer) RegisterHandler(taskType string, handler MessageHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[taskType] = handler
	log.Printf("[RocketMQ] 注册处理器: taskType=%s", taskType)
}

func (c *Consumer) Subscribe(topic string) error {
	selector := consumer.MessageSelector{
		Type:       consumer.TAG,
		Expression: "*",
	}

	err := c.consumer.Subscribe(topic, selector, func(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		for _, msg := range msgs {
			if err := c.handleMessage(ctx, msg); err != nil {
				log.Printf("[RocketMQ] 消息处理失败: msgId=%s, error=%v", msg.MsgId, err)
				return consumer.ConsumeRetryLater, nil
			}
		}
		return consumer.ConsumeSuccess, nil
	})

	if err != nil {
		return fmt.Errorf("订阅topic失败: %v", err)
	}

	log.Printf("[RocketMQ] 已订阅topic: %s", topic)
	return nil
}

func (c *Consumer) handleMessage(ctx context.Context, msg *primitive.MessageExt) error {
	var taskMsg TaskMessage
	if err := json.Unmarshal(msg.Body, &taskMsg); err != nil {
		return fmt.Errorf("解析消息失败: %v", err)
	}

	c.mu.RLock()
	handler, ok := c.handlers[taskMsg.TaskType]
	c.mu.RUnlock()

	if !ok {
		log.Printf("[RocketMQ] 未找到处理器: taskType=%s", taskMsg.TaskType)
		return nil
	}

	return handler(ctx, taskMsg.Payload)
}

func (c *Consumer) Start() error {
	return c.StartWithRetry(0, 0)
}

func (c *Consumer) StartWithRetry(maxRetries int, retryInterval time.Duration) error {
	if maxRetries == 0 {
		maxRetries = 30
	}
	if retryInterval == 0 {
		retryInterval = 2 * time.Second
	}

	for i := 0; i < maxRetries; i++ {
		err := c.consumer.Start()
		if err == nil {
			log.Printf("[RocketMQ] Consumer已启动, Group: %s", c.group)
			return nil
		}

		errStr := err.Error()
		if strings.Contains(errStr, "route info not found") || strings.Contains(errStr, "topic not exist") {
			if i < maxRetries-1 {
				log.Printf("[RocketMQ] Topic尚未创建，%d秒后重试 (%d/%d)...", int(retryInterval.Seconds()), i+1, maxRetries)
				time.Sleep(retryInterval)
				continue
			}
		}
		return fmt.Errorf("启动Consumer失败: %v", err)
	}
	return fmt.Errorf("启动Consumer失败: 超过最大重试次数")
}

func (c *Consumer) Close() error {
	if c.consumer != nil {
		return c.consumer.Shutdown()
	}
	return nil
}

type CallbackPayload struct {
	TaskID       string      `json:"task_id"`
	Status       string      `json:"status"`
	Result       interface{} `json:"result"`
	ErrorMessage string      `json:"error_message"`
}

func (p *Producer) SendCallback(topic, taskID, status string, result interface{}, errorMsg string) error {
	payload := CallbackPayload{
		TaskID:       taskID,
		Status:       status,
		Result:       result,
		ErrorMessage: errorMsg,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化回调载荷失败: %v", err)
	}

	_, err = p.SendMessage(topic, "task_callback", payloadBytes)
	return err
}