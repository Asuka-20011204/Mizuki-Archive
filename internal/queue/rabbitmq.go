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
	"golang.org/x/sync/errgroup"
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

// deferredHandlerError 标记业务暂时无法处理的消息，消费者应延迟重发而不是 NACK 回队首。
type deferredHandlerError interface {
	DeferProcessing() bool
}

// RabbitMQ 管理发布通道和消费通道；两者分离避免发布确认阻塞 ACK。
type RabbitMQ struct {
	connection *amqp.Connection
	publisher  *amqp.Channel
	consumer   *amqp.Channel
	returned   <-chan amqp.Return
	publishMu  sync.Mutex
	consumerMu sync.Mutex
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

// ConsumeJobs 启动固定数量的消费者；QoS 限制未确认消息，业务完成后才 ACK。
func (broker *RabbitMQ) ConsumeJobs(ctx context.Context, handler Handler, prefetch int) error {
	if prefetch <= 0 {
		prefetch = 1
	}
	if prefetch > 4 {
		prefetch = 4
	}
	if err := broker.consumer.Qos(prefetch, 0, false); err != nil {
		return fmt.Errorf("set RabbitMQ prefetch: %w", err)
	}
	group, workerContext := errgroup.WithContext(ctx)
	deliveries, err := broker.consumer.ConsumeWithContext(workerContext, broker.queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume RabbitMQ jobs: %w", err)
	}
	for index := 0; index < prefetch; index++ {
		group.Go(func() error {
			for {
				select {
				case <-workerContext.Done():
					return workerContext.Err()
				case delivery, open := <-deliveries:
					if !open {
						if workerContext.Err() != nil {
							return workerContext.Err()
						}
						return errors.New("RabbitMQ consumer channel closed")
					}
					if err := broker.consumeDelivery(workerContext, handler, delivery); err != nil {
						return err
					}
				}
			}
		})
	}
	return group.Wait()
}

// consumeDelivery 隔离一条投递的校验、有限重试与确认逻辑；失败时不误 ACK 原消息。
func (broker *RabbitMQ) consumeDelivery(ctx context.Context, handler Handler, delivery amqp.Delivery) error {
	message, err := decodeJobMessage(delivery.Body)
	if err != nil {
		if err := broker.finishDelivery(delivery, false, false); err != nil {
			return fmt.Errorf("dead-letter invalid processing message: %w", err)
		}
		return nil
	}
	retryCount, valid := headerInt(delivery.Headers, "x-broker-retry")
	if !valid {
		if err := broker.finishDelivery(delivery, false, false); err != nil {
			return fmt.Errorf("dead-letter invalid processing retry: %w", err)
		}
		return nil
	}
	finished, err := handler(ctx, message.JobID)
	if err != nil {
		if deferred, ok := err.(deferredHandlerError); ok && deferred.DeferProcessing() {
			return broker.deferDelivery(ctx, delivery, message.JobID, retryCount)
		}
		if retryCount < maxBrokerRetry {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<retryCount) * time.Second):
			}
			if publishErr := broker.publish(ctx, broker.queue, message.JobID, retryCount+1); publishErr == nil {
				if err := broker.finishDelivery(delivery, true, false); err != nil {
					return fmt.Errorf("ack republished processing message: %w", err)
				}
				return nil
			}
			// 无法确认重发时不能死信原消息，Broker 恢复后应重新投递。
			return errors.New("cannot confirm processing message retry")
		}
		if err := broker.finishDelivery(delivery, false, false); err != nil {
			return fmt.Errorf("dead-letter failed processing message: %w", err)
		}
		return nil
	}
	if !finished {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
		if err := broker.finishDelivery(delivery, false, true); err != nil {
			return fmt.Errorf("requeue busy processing message: %w", err)
		}
		return nil
	}
	if err := broker.finishDelivery(delivery, true, false); err != nil {
		return fmt.Errorf("ack RabbitMQ processing message: %w", err)
	}
	return nil
}

// deferDelivery 确认当前投递后再延迟发布同一任务，避免未确认消息占满 prefetch 或反复回到队首。
func (broker *RabbitMQ) deferDelivery(ctx context.Context, delivery amqp.Delivery, jobID string, retryCount int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
	}
	if err := broker.publish(ctx, broker.queue, jobID, retryCount); err != nil {
		return fmt.Errorf("defer processing message: %w", err)
	}
	if err := broker.finishDelivery(delivery, true, false); err != nil {
		return fmt.Errorf("ack deferred processing message: %w", err)
	}
	return nil
}

// finishDelivery 串行化共享消费通道的 ACK/NACK，避免并发 Worker 交错提交确认帧。
func (broker *RabbitMQ) finishDelivery(delivery amqp.Delivery, acknowledge, requeue bool) error {
	broker.consumerMu.Lock()
	defer broker.consumerMu.Unlock()
	if acknowledge {
		return delivery.Ack(false)
	}
	return delivery.Nack(false, requeue)
}

// PublisherClosed 标记发布通道失效；消费通道仍存活时也必须重建整条 Broker 连接。
func (broker *RabbitMQ) PublisherClosed() bool {
	return broker.publisher.IsClosed() || broker.connection.IsClosed()
}

// Close 限时关闭整条连接，避免取消消费的内部协程与通道关闭同时等待响应；未确认消息由 Broker 重排。
func (broker *RabbitMQ) Close() error {
	if err := broker.connection.CloseDeadline(time.Now().Add(3 * time.Second)); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return err
	}
	return nil
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
