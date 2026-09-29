// Package queue 封装 RabbitMQ 的连接、发布确认、消费确认和死信边界。
package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	defaultExchange = "mizuki.archive.processing.v1"
	defaultQueue    = "mizuki.archive.processing.v1"
	defaultDead     = "mizuki.archive.processing.v1.dead"
	maxBrokerRetry  = 3
)

// JobMessage 是 RabbitMQ 中唯一允许出现的业务消息，避免把文件内容和本地路径送入消息系统。
type JobMessage struct {
	Version int    `json:"version"`
	JobID   string `json:"job_id"`
}

// Handler 是消费者收到合法任务 ID 后执行的业务回调；false 表示任务租约尚未到期，不应 ACK。
type Handler func(context.Context, string) (bool, error)

// RabbitMQ 管理发布通道和消费通道；两者分离避免发布确认阻塞 ACK。
type RabbitMQ struct {
	connection *amqp.Connection
	publisher  *amqp.Channel
	consumer   *amqp.Channel
	returned   <-chan amqp.Return
	publishMu  sync.Mutex
	exchange   string
	queue      string
	deadQueue  string
}

// NewRabbitMQ 建立持久交换机、主队列和隔离队列，并开启发布确认。
func NewRabbitMQ(url, exchange, queueName, deadQueue string) (*RabbitMQ, error) {
	if url == "" {
		return nil, errors.New("empty RabbitMQ URL")
	}
	if exchange == "" {
		exchange = defaultExchange
	}
	if queueName == "" {
		queueName = defaultQueue
	}
	if deadQueue == "" {
		deadQueue = defaultDead
	}
	connection, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial RabbitMQ: %w", err)
	}
	publisher, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("open RabbitMQ publisher channel: %w", err)
	}
	consumer, err := connection.Channel()
	if err != nil {
		_ = publisher.Close()
		_ = connection.Close()
		return nil, fmt.Errorf("open RabbitMQ consumer channel: %w", err)
	}
	closeOnError := func(reason error) (*RabbitMQ, error) {
		_ = consumer.Close()
		_ = publisher.Close()
		_ = connection.Close()
		return nil, reason
	}
	if err := publisher.Confirm(false); err != nil {
		return closeOnError(fmt.Errorf("enable RabbitMQ publisher confirms: %w", err))
	}
	returned := publisher.NotifyReturn(make(chan amqp.Return, 1))
	if err := publisher.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
		return closeOnError(fmt.Errorf("declare RabbitMQ exchange: %w", err))
	}
	if _, err := publisher.QueueDeclare(deadQueue, true, false, false, false, nil); err != nil {
		return closeOnError(fmt.Errorf("declare RabbitMQ dead queue: %w", err))
	}
	if _, err := publisher.QueueDeclare(queueName, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    exchange,
		"x-dead-letter-routing-key": deadQueue,
	}); err != nil {
		return closeOnError(fmt.Errorf("declare RabbitMQ job queue: %w", err))
	}
	if err := publisher.QueueBind(queueName, queueName, exchange, false, nil); err != nil {
		return closeOnError(fmt.Errorf("bind RabbitMQ job queue: %w", err))
	}
	if err := publisher.QueueBind(deadQueue, deadQueue, exchange, false, nil); err != nil {
		return closeOnError(fmt.Errorf("bind RabbitMQ dead queue: %w", err))
	}
	return &RabbitMQ{connection: connection, publisher: publisher, consumer: consumer, returned: returned, exchange: exchange, queue: queueName, deadQueue: deadQueue}, nil
}

// PublishJob 编码任务 ID 并等待 RabbitMQ 的发布确认，确认前不会返回成功。
func (broker *RabbitMQ) PublishJob(ctx context.Context, jobID string) error {
	return broker.publish(ctx, broker.queue, jobID, 0)
}

// publish 通过单独的路由键发送任务，并把传输重试次数放在消息头而不是业务任务状态中。
func (broker *RabbitMQ) publish(ctx context.Context, routingKey, jobID string, retryCount int) error {
	if !validJobID(jobID) {
		return errors.New("invalid processing job ID")
	}
	body, err := json.Marshal(JobMessage{Version: 1, JobID: jobID})
	if err != nil {
		return fmt.Errorf("encode processing message: %w", err)
	}
	// 通道上同一时间只留一个发布确认；mandatory 返回必须在 ACK 之前检查，避免误把未路由消息标成成功。
	broker.publishMu.Lock()
	defer broker.publishMu.Unlock()
	confirmation, err := broker.publisher.PublishWithDeferredConfirmWithContext(ctx, broker.exchange, routingKey, true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body, Headers: amqp.Table{"x-broker-retry": retryCount}})
	if err != nil {
		return fmt.Errorf("publish processing message: %w", err)
	}
	if confirmation == nil {
		return errors.New("RabbitMQ publisher confirms unavailable")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	acknowledged, err := confirmation.WaitContext(waitCtx)
	if err != nil {
		return fmt.Errorf("wait RabbitMQ publisher confirmation: %w", err)
	}
	select {
	case returned, open := <-broker.returned:
		if open && returned.ReplyCode != 0 {
			return errors.New("RabbitMQ processing message was not routed")
		}
	default:
	}
	if !acknowledged {
		return errors.New("RabbitMQ rejected processing message")
	}
	return nil
}

// ConsumeJobs 消费持久消息；业务成功后 ACK，临时错误有限重发，超过次数进入隔离队列。
func (broker *RabbitMQ) ConsumeJobs(ctx context.Context, handler Handler, prefetch int) error {
	if prefetch <= 0 {
		prefetch = 1
	}
	if err := broker.consumer.Qos(prefetch, 0, false); err != nil {
		return fmt.Errorf("set RabbitMQ prefetch: %w", err)
	}
	deliveries, err := broker.consumer.ConsumeWithContext(ctx, broker.queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume RabbitMQ jobs: %w", err)
	}
	for delivery := range deliveries {
		message, err := decodeJobMessage(delivery.Body)
		if err != nil {
			if err := delivery.Nack(false, false); err != nil {
				return fmt.Errorf("dead-letter invalid processing message: %w", err)
			}
			continue
		}
		retryCount, valid := headerInt(delivery.Headers, "x-broker-retry")
		if !valid {
			if err := delivery.Nack(false, false); err != nil {
				return fmt.Errorf("dead-letter invalid processing retry: %w", err)
			}
			continue
		}
		finished, err := handler(ctx, message.JobID)
		if err != nil {
			if retryCount < maxBrokerRetry {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(1<<retryCount) * time.Second):
				}
				if publishErr := broker.publish(ctx, broker.queue, message.JobID, retryCount+1); publishErr == nil {
					if err := delivery.Ack(false); err != nil {
						return fmt.Errorf("ack republished processing message: %w", err)
					}
					continue
				}
				// 无法确认重发时不能死信原消息，Broker 恢复后应重新投递。
				return errors.New("cannot confirm processing message retry")
			}
			if err := delivery.Nack(false, false); err != nil {
				return fmt.Errorf("dead-letter failed processing message: %w", err)
			}
			continue
		}
		if !finished {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			if err := delivery.Nack(false, true); err != nil {
				return fmt.Errorf("requeue busy processing message: %w", err)
			}
			continue
		}
		if err := delivery.Ack(false); err != nil {
			return fmt.Errorf("ack RabbitMQ processing message: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("RabbitMQ consumer channel closed")
}

// Close 按发布、消费、连接顺序关闭资源，避免后台协程继续使用已释放通道。
// PublisherClosed 标记发布通道失效；消费通道仍存活时也必须重建整条 Broker 连接。
func (broker *RabbitMQ) PublisherClosed() bool {
	return broker.publisher.IsClosed() || broker.connection.IsClosed()
}

// Close 按发布、消费、连接顺序关闭资源，避免后台协程继续使用已释放通道。
func (broker *RabbitMQ) Close() error {
	var first error
	if err := broker.consumer.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		first = err
	}
	if err := broker.publisher.Close(); err != nil && first == nil && !errors.Is(err, amqp.ErrClosed) {
		first = err
	}
	if err := broker.connection.Close(); err != nil && first == nil && !errors.Is(err, amqp.ErrClosed) {
		first = err
	}
	return first
}

// decodeJobMessage 严格校验版本和任务 ID，拒绝未知字段与携带正文的消息。
func decodeJobMessage(body []byte) (JobMessage, error) {
	if len(body) > 256 {
		return JobMessage{}, errors.New("processing message too large")
	}
	var message JobMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil || message.Version != 1 || !validJobID(message.JobID) {
		return JobMessage{}, errors.New("invalid processing message")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return JobMessage{}, errors.New("processing message contains trailing data")
	}
	return message, nil
}

// validJobID 只接受服务端生成的十六进制任务 ID，不让消息正文携带文件路径等额外输入。
func validJobID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

// headerInt 只接受 0–3 次传输重试，拒绝负数及异常类型以免位移导致 Worker 崩溃。
func headerInt(headers amqp.Table, name string) (int, bool) {
	value, ok := headers[name]
	if !ok {
		return 0, true
	}
	var count int64
	switch typed := value.(type) {
	case int:
		count = int64(typed)
	case int32:
		count = int64(typed)
	case int64:
		count = typed
	default:
		return 0, false
	}
	return int(count), count >= 0 && count <= maxBrokerRetry
}
