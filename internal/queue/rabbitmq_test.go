package queue

import (
	"context"
	"os"
	"strings"
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
