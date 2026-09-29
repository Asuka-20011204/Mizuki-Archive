package queue

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TestDecodeJobMessage 拒绝未知字段、尾随内容和非法 ID，消息不能携带私有文件数据。
func TestDecodeJobMessage(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, test := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"合法任务", `{"version":1,"job_id":"` + id + `"}`, true},
		{"附带私有路径", `{"version":1,"job_id":"` + id + `","path":"private"}`, false},
		{"连续消息", `{"version":1,"job_id":"` + id + `"}{}`, false},
		{"无效版本", `{"version":2,"job_id":"` + id + `"}`, false},
		{"无效编号", `{"version":1,"job_id":"../secret"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeJobMessage([]byte(test.body))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t, error=%v", test.valid, err)
			}
		})
	}
}

// TestRetryHeader 拒绝负数、非整数和越界次数，避免处理失败时无效移位。
func TestRetryHeader(t *testing.T) {
	for _, test := range []struct {
		value any
		valid bool
	}{
		{int32(0), true}, {int64(3), true}, {int32(-1), false}, {int64(99999999), false}, {"-1", false},
	} {
		_, valid := headerInt(amqp.Table{"x-broker-retry": test.value}, "x-broker-retry")
		if valid != test.valid {
			t.Fatalf("value=%v, valid=%t", test.value, valid)
		}
	}
}

// TestRabbitMQPublishConsume 在显式隔离 Broker 中验证持久发布确认与成功后 ACK。
func TestRabbitMQPublishConsume(t *testing.T) {
	url := os.Getenv("MIZUKI_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("仅在设置隔离 RabbitMQ 地址时运行")
	}
	name := "mizuki.test." + strings.ReplaceAll(t.Name(), "/", ".") + "." + time.Now().UTC().Format("150405.000000000")
	broker, err := NewRabbitMQ(url, name, name+".jobs", name+".dead")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = broker.publisher.QueueDelete(name+".jobs", false, false, false)
		_, _ = broker.publisher.QueueDelete(name+".dead", false, false, false)
		_ = broker.publisher.ExchangeDelete(name, false, false)
		_ = broker.Close()
	})
	id := strings.Repeat("b", 32)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := broker.PublishJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		_ = broker.ConsumeJobs(ctx, func(_ context.Context, jobID string) (bool, error) {
			got <- jobID
			return true, nil
		}, 1)
	}()
	select {
	case jobID := <-got:
		if jobID != id {
			t.Fatalf("received %s", jobID)
		}
	case <-ctx.Done():
		t.Fatal("RabbitMQ message not consumed")
	}
}

// TestRabbitMQDeadLetter 使用真实 Broker 验证有限重试后消息进入隔离队列，而不是无限重发。
func TestRabbitMQDeadLetter(t *testing.T) {
	url := os.Getenv("MIZUKI_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("仅在设置隔离 RabbitMQ 地址时运行")
	}
	name := "mizuki.test.dead." + time.Now().UTC().Format("150405.000000000")
	broker, err := NewRabbitMQ(url, name, name+".jobs", name+".dead")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = broker.publisher.QueueDelete(name+".jobs", false, false, false)
		_, _ = broker.publisher.QueueDelete(name+".dead", false, false, false)
		_ = broker.publisher.ExchangeDelete(name, false, false)
		_ = broker.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	var attempts atomic.Int32
	consumerStopped := make(chan struct{})
	go func() {
		defer close(consumerStopped)
		_ = broker.ConsumeJobs(ctx, func(context.Context, string) (bool, error) {
			attempts.Add(1)
			return false, errors.New("模拟暂时失败")
		}, 1)
	}()
	jobID := strings.Repeat("c", 32)
	if err := broker.PublishJob(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("有限重试后消息未进入隔离队列")
		case <-ticker.C:
			message, available, err := broker.publisher.Get(name+".dead", true)
			if err != nil {
				t.Fatal(err)
			}
			if available {
				if !strings.Contains(string(message.Body), jobID) || attempts.Load() != maxBrokerRetry+1 {
					t.Fatalf("隔离队列内容或处理次数不符: attempts=%d", attempts.Load())
				}
				cancel()
				<-consumerStopped
				return
			}
		}
	}
}

// TestRabbitMQRejectsInvalidMessages 验证非法业务体及负数重试头都进入死信，不调用处理器。
func TestRabbitMQRejectsInvalidMessages(t *testing.T) {
	url := os.Getenv("MIZUKI_TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("仅在设置隔离 RabbitMQ 地址时运行")
	}
	name := "mizuki.test.invalid." + time.Now().UTC().Format("150405.000000000")
	broker, err := NewRabbitMQ(url, name, name+".jobs", name+".dead")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = broker.publisher.QueueDelete(name+".jobs", false, false, false)
		_, _ = broker.publisher.QueueDelete(name+".dead", false, false, false)
		_ = broker.publisher.ExchangeDelete(name, false, false)
		_ = broker.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var called atomic.Int32
	consumerStopped := make(chan struct{})
	go func() {
		defer close(consumerStopped)
		_ = broker.ConsumeJobs(ctx, func(context.Context, string) (bool, error) {
			called.Add(1)
			return true, nil
		}, 1)
	}()
	for _, publishing := range []amqp.Publishing{
		{Body: []byte(`{"job_id":"path/to/private/file"}`), DeliveryMode: amqp.Persistent},
		{Body: []byte(`{"version":1,"job_id":"` + strings.Repeat("d", 32) + `"}`), DeliveryMode: amqp.Persistent, Headers: amqp.Table{"x-broker-retry": int32(-1)}},
	} {
		confirmation, err := broker.publisher.PublishWithDeferredConfirmWithContext(ctx, name, name+".jobs", true, false, publishing)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := confirmation.WaitContext(ctx); err != nil || !ok {
			t.Fatalf("测试消息未确认: %t, %v", ok, err)
		}
	}
	dead := 0
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for dead < 2 {
		select {
		case <-ctx.Done():
			t.Fatalf("只有 %d 条非法消息进入隔离队列", dead)
		case <-ticker.C:
			_, available, err := broker.publisher.Get(name+".dead", true)
			if err != nil {
				t.Fatal(err)
			}
			if available {
				dead++
			}
		}
	}
	if called.Load() != 0 {
		t.Fatal("非法消息调用了业务处理器")
	}
	cancel()
	<-consumerStopped
	if broker.PublisherClosed() {
		t.Fatal("健康发布通道被误判为关闭")
	}
	if err := broker.publish(ctx, name+".jobs", "../invalid", 0); err == nil {
		t.Fatal("不应发布非法任务 ID")
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	if !broker.PublisherClosed() {
		t.Fatal("已关闭的发布通道未被识别")
	}
}
