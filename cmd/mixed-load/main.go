package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// fixture 描述一种合成资料及其对应的处理任务类型。
type fixture struct {
	kind     string
	filename string
	taskType string
	content  []byte
}

// jobResponse 是创建或读取单个任务时服务端返回的最小结构。
type jobResponse struct {
	ID         string `json:"id"`
	ResourceID string `json:"resource_id"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Attempts   int    `json:"attempts"`
	Asset      *struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"asset,omitempty"`
}

// resourceResponse 是上传接口返回的资料标识。
type resourceResponse struct {
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
}

// jobEnvelope 是单任务接口的响应包装。
type jobEnvelope struct {
	Data jobResponse `json:"data"`
}

// httpMetric 记录单次 HTTP 请求，响应正文只用于识别错误类别，不写入结果文件。
type httpMetric struct {
	Timestamp   time.Time
	Operation   string
	Kind        string
	Status      int
	ElapsedMS   float64
	Error       string
	Expected429 bool
	Timeout     bool
}

// taskMetric 记录任务最终状态及服务端时间戳，便于计算排队和端到端耗时。
type taskMetric struct {
	Timestamp  time.Time
	JobID      string
	ResourceID string
	Kind       string
	TaskType   string
	Status     string
	Attempts   int
	AssetID    string
	CreatedAt  string
	StartedAt  string
	FinishedAt string
	Error      string
}

// pollRequest 描述一个已接受、等待轮询的任务，交给有界轮询队列处理。
type pollRequest struct {
	client     *http.Client
	baseURL    string
	origin     string
	jobID      string
	resourceID string
	input      fixture
	recorder   *recorder
}

// recorder 保护并发压测 goroutine 写入的结果和资料 ID。
type recorder struct {
	mu       sync.Mutex
	http     []httpMetric
	tasks    []taskMetric
	resource []string
	accepted int
}

// recordHTTP 保存一次 HTTP 指标。
func (r *recorder) recordHTTP(metric httpMetric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.http = append(r.http, metric)
}

// addResource 保存一个可供读请求下载的资料 ID。
func (r *recorder) addResource(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resource = append(r.resource, id)
}

// recordAccepted 记录服务端已接受并创建成功的任务数量。
func (r *recorder) recordAccepted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accepted++
}

// latestResource 返回当前已上传资料中的一个 ID。
func (r *recorder) latestResource() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.resource) == 0 {
		return ""
	}
	return r.resource[len(r.resource)-1]
}

// recordTask 保存任务最终状态。
func (r *recorder) recordTask(metric taskMetric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, metric)
}

// loadFixtures 读取固定文件名，拒绝从仓库或正式资料目录猜测输入。
func loadFixtures(directory string) ([]fixture, error) {
	definitions := []struct {
		name string
		kind string
		task string
	}{
		{"sample.txt", "text", "extract_text"},
		{"sample.pdf", "pdf", "extract_text"},
		{"sample.png", "image", "generate_thumbnail"},
	}
	fixtures := make([]fixture, 0, len(definitions))
	for _, definition := range definitions {
		path := filepath.Join(directory, definition.name)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取测试文件 %s: %w", path, err)
		}
		if len(content) == 0 {
			return nil, fmt.Errorf("测试文件为空: %s", path)
		}
		fixtures = append(fixtures, fixture{kind: definition.kind, filename: definition.name, taskType: definition.task, content: content})
	}
	return fixtures, nil
}

// newHTTPClient 创建带独立 Cookie Jar 的压测客户端。
func newHTTPClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Cookie Jar: %w", err)
	}
	return &http.Client{Jar: jar, Timeout: 45 * time.Second}, nil
}

// normalizeBaseURL 校验压测目标只包含协议、主机和可选端口。
func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("无效的 base URL: %s", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// request 执行同源 API 请求并记录耗时、状态码和 queue_full 分类。
func request(ctx context.Context, client *http.Client, baseURL, origin, operation, kind, method, path string, body io.Reader, contentType string, recorder *recorder) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Origin", origin)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	started := time.Now()
	response, err := client.Do(request)
	elapsed := time.Since(started)
	metric := httpMetric{Timestamp: started.UTC(), Operation: operation, Kind: kind, ElapsedMS: float64(elapsed.Microseconds()) / 1000}
	if err != nil {
		metric.Error = err.Error()
		metric.Timeout = isTimeoutError(err)
		recorder.recordHTTP(metric)
		return nil, 0, err
	}
	defer response.Body.Close()
	var responseBody []byte
	var readErr error
	if operation == "download" {
		_, readErr = io.Copy(io.Discard, response.Body)
	} else {
		responseBody, readErr = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	}
	metric.Status = response.StatusCode
	if response.StatusCode == http.StatusTooManyRequests && bytes.Contains(responseBody, []byte("queue_full")) {
		metric.Expected429 = true
	}
	if readErr != nil {
		metric.Error = readErr.Error()
		metric.Timeout = isTimeoutError(readErr)
	}
	recorder.recordHTTP(metric)
	if readErr != nil {
		return responseBody, response.StatusCode, readErr
	}
	return responseBody, response.StatusCode, nil
}

// isTimeoutError 只把上下文截止或网络层明确报告的超时计入 timeout。
func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

// login 使用压测账号创建当前客户端的会话 Cookie。
func login(ctx context.Context, client *http.Client, baseURL, origin, username, password string, recorder *recorder) error {
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return err
	}
	body, status, err := request(ctx, client, baseURL, origin, "login", "account", http.MethodPost, "/api/login", bytes.NewReader(payload), "application/json", recorder)
	if err != nil {
		return fmt.Errorf("登录请求失败: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("登录返回 HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	return nil
}

// upload 上传一份合成资料并返回资源 ID。
func upload(ctx context.Context, client *http.Client, baseURL, origin string, input fixture, recorder *recorder) (string, error) {
	var buffer bytes.Buffer
	multipartWriter := multipart.NewWriter(&buffer)
	part, err := multipartWriter.CreateFormFile("file", input.filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(input.content); err != nil {
		return "", err
	}
	if err := multipartWriter.Close(); err != nil {
		return "", err
	}
	body, status, err := request(ctx, client, baseURL, origin, "upload", input.kind, http.MethodPost, "/api/resources", &buffer, multipartWriter.FormDataContentType(), recorder)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("上传返回 HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	var response resourceResponse
	if err := json.Unmarshal(body, &response); err != nil || response.Data.ID == "" {
		return "", fmt.Errorf("上传响应没有 data.id")
	}
	return response.Data.ID, nil
}

// createJob 为资料创建白名单处理任务并返回任务 ID。
func createJob(ctx context.Context, client *http.Client, baseURL, origin, resourceID string, input fixture, recorder *recorder) (string, error) {
	payload, err := json.Marshal(map[string]string{"type": input.taskType})
	if err != nil {
		return "", err
	}
	body, status, err := request(ctx, client, baseURL, origin, "create_job", input.kind, http.MethodPost, "/api/resources/"+url.PathEscape(resourceID)+"/jobs", bytes.NewReader(payload), "application/json", recorder)
	if err != nil {
		return "", err
	}
	if status != http.StatusAccepted {
		return "", fmt.Errorf("创建任务返回 HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	var response jobEnvelope
	if err := json.Unmarshal(body, &response); err != nil || response.Data.ID == "" {
		return "", fmt.Errorf("任务响应没有 data.id")
	}
	return response.Data.ID, nil
}

// pollJob 轮询任务到最终状态并记录服务端时间戳与派生产物 ID。
func pollJob(ctx context.Context, client *http.Client, baseURL, origin, jobID, resourceID string, input fixture, recorder *recorder) {
	started := time.Now().UTC()
	metric := taskMetric{Timestamp: started, JobID: jobID, ResourceID: resourceID, Kind: input.kind, TaskType: input.taskType}
	for {
		if ctx.Err() != nil {
			metric.Error = ctx.Err().Error()
			metric.Status = "unknown"
			recorder.recordTask(metric)
			return
		}
		body, status, err := request(ctx, client, baseURL, origin, "poll_job", input.kind, http.MethodGet, "/api/jobs/"+url.PathEscape(jobID), nil, "", recorder)
		if err == nil && status == http.StatusOK {
			var response jobEnvelope
			if json.Unmarshal(body, &response) == nil {
				job := response.Data
				metric.Status = job.Status
				metric.Attempts = job.Attempts
				metric.CreatedAt = job.CreatedAt
				metric.StartedAt = job.StartedAt
				metric.FinishedAt = job.FinishedAt
				if job.Asset != nil {
					metric.AssetID = job.Asset.ID
				}
				if job.Status == "succeeded" || job.Status == "failed" {
					recorder.recordTask(metric)
					return
				}
			}
		} else if err != nil {
			metric.Error = err.Error()
		} else {
			metric.Error = fmt.Sprintf("poll returned HTTP %d", status)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// pollLoop 从有界队列取任务，避免高到达速率产生无限轮询 goroutine。
func pollLoop(ctx context.Context, requests <-chan pollRequest, wg *sync.WaitGroup) {
	defer wg.Done()
	for request := range requests {
		pollJob(ctx, request.client, request.baseURL, request.origin, request.jobID, request.resourceID, request.input, request.recorder)
	}
}

// submitLoop 按固定到达速率提交三类资料，并等待提交 goroutine 和任务轮询完成。
func submitLoop(ctx context.Context, pollCtx context.Context, pollRequests chan<- pollRequest, client *http.Client, baseURL, origin, username, password string, fixtures []fixture, rate float64, duration time.Duration, recorder *recorder) {
	if err := login(ctx, client, baseURL, origin, username, password, recorder); err != nil {
		recorder.recordTask(taskMetric{Timestamp: time.Now().UTC(), Status: "login_failed", Error: err.Error()})
		return
	}
	interval := time.Duration(float64(time.Second) / rate)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	deadline := time.Now().Add(duration)
	var submitWG sync.WaitGroup
	defer submitWG.Wait()
	semaphore := make(chan struct{}, 16)
	index := 0
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		input := fixtures[index%len(fixtures)]
		index++
		semaphore <- struct{}{}
		submitWG.Add(1)
		go func() {
			defer submitWG.Done()
			defer func() { <-semaphore }()
			resourceID, err := upload(ctx, client, baseURL, origin, input, recorder)
			if err != nil {
				return
			}
			recorder.addResource(resourceID)
			jobID, err := createJob(ctx, client, baseURL, origin, resourceID, input, recorder)
			if err != nil {
				return
			}
			recorder.recordAccepted()
			select {
			case pollRequests <- pollRequest{
				client: client, baseURL: baseURL, origin: origin, jobID: jobID,
				resourceID: resourceID, input: input, recorder: recorder,
			}:
			case <-pollCtx.Done():
				recorder.recordTask(taskMetric{
					Timestamp:  time.Now().UTC(),
					JobID:      jobID,
					ResourceID: resourceID,
					Kind:       input.kind,
					TaskType:   input.taskType,
					Status:     "unknown",
					Error:      pollCtx.Err().Error(),
				})
				return
			}
		}()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

// readLoop 并发执行资料列表、关键词搜索和已有资料下载请求。
func readLoop(ctx context.Context, client *http.Client, baseURL, origin, username, password string, workerID int, recorder *recorder) {
	if err := login(ctx, client, baseURL, origin, username, password, recorder); err != nil {
		recorder.recordTask(taskMetric{Timestamp: time.Now().UTC(), Status: "read_login_failed", Error: err.Error()})
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	requestCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requestCount++
			_, _, _ = request(ctx, client, baseURL, origin, "list", "read", http.MethodGet, "/api/resources", nil, "", recorder)
			_, _, _ = request(ctx, client, baseURL, origin, "search", "read", http.MethodGet, "/api/search?q=MIZUKI_BENCHMARK_SYNTHETIC_TEXT", nil, "", recorder)
			if requestCount%3 == workerID%3 {
				if resourceID := recorder.latestResource(); resourceID != "" {
					_, _, _ = request(ctx, client, baseURL, origin, "download", "read", http.MethodGet, "/api/resources/"+url.PathEscape(resourceID)+"/download", nil, "", recorder)
				}
			}
		}
	}
}

// percentile 计算已排序样本的线性插值分位点。
func percentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	position := fraction * float64(len(values)-1)
	lower := int(position)
	upper := lower + 1
	if upper >= len(values) {
		return values[lower]
	}
	weight := position - float64(lower)
	return values[lower] + (values[upper]-values[lower])*weight
}

// summarizeHTTP 按操作汇总请求数、错误数、429 分类和延迟分位点。
func summarizeHTTP(metrics []httpMetric) map[string]map[string]any {
	grouped := make(map[string][]httpMetric)
	for _, metric := range metrics {
		grouped[metric.Operation] = append(grouped[metric.Operation], metric)
	}
	result := make(map[string]map[string]any, len(grouped))
	for operation, samples := range grouped {
		latencies := make([]float64, 0, len(samples))
		statusCounts := make(map[string]int)
		expected429 := 0
		unexpected429 := 0
		timeouts := 0
		for _, sample := range samples {
			latencies = append(latencies, sample.ElapsedMS)
			statusCounts[strconv.Itoa(sample.Status)]++
			if sample.Expected429 {
				expected429++
			} else if sample.Status == http.StatusTooManyRequests {
				unexpected429++
			}
			if sample.Timeout {
				timeouts++
			}
		}
		sort.Float64s(latencies)
		result[operation] = map[string]any{
			"count":          len(samples),
			"status_counts":  statusCounts,
			"expected_429":   expected429,
			"unexpected_429": unexpected429,
			"timeouts":       timeouts,
			"p50_ms":         percentile(latencies, 0.50),
			"p95_ms":         percentile(latencies, 0.95),
			"p99_ms":         percentile(latencies, 0.99),
		}
	}
	return result
}

// summarizeHTTPTotal 汇总全部 HTTP 样本，直接给出吞吐和错误分类。
func summarizeHTTPTotal(metrics []httpMetric, durationSeconds float64) map[string]any {
	total := map[string]any{
		"count":               len(metrics),
		"requests_per_second": 0.0,
		"five_xx":             0,
		"expected_429":        0,
		"unexpected_429":      0,
		"timeouts":            0,
	}
	if durationSeconds > 0 {
		total["requests_per_second"] = float64(len(metrics)) / durationSeconds
	}
	for _, metric := range metrics {
		if metric.Status >= 500 && metric.Status <= 599 {
			total["five_xx"] = total["five_xx"].(int) + 1
		}
		if metric.Expected429 {
			total["expected_429"] = total["expected_429"].(int) + 1
		} else if metric.Status == http.StatusTooManyRequests {
			total["unexpected_429"] = total["unexpected_429"].(int) + 1
		}
		if metric.Timeout {
			total["timeouts"] = total["timeouts"].(int) + 1
		}
	}
	return total
}

// filterHTTPMetrics 按请求开始时间切分负载窗口和排空窗口，避免吞吐分母混入排空请求。
func filterHTTPMetrics(metrics []httpMetric, start, end time.Time) []httpMetric {
	filtered := make([]httpMetric, 0, len(metrics))
	for _, metric := range metrics {
		if !metric.Timestamp.Before(start) && metric.Timestamp.Before(end) {
			filtered = append(filtered, metric)
		}
	}
	return filtered
}

// writeResults 将原始 HTTP、任务明细和 JSON 汇总写入独立结果目录。
func writeResults(directory string, recorder *recorder, started, loadFinished, finished time.Time, rate float64, duration time.Duration) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	httpFile, err := os.Create(filepath.Join(directory, "http.csv"))
	if err != nil {
		return err
	}
	defer httpFile.Close()
	httpWriter := csv.NewWriter(httpFile)
	_ = httpWriter.Write([]string{"timestamp", "operation", "kind", "status", "elapsed_ms", "expected_429", "timeout", "error"})
	for _, metric := range recorder.http {
		_ = httpWriter.Write([]string{metric.Timestamp.Format(time.RFC3339Nano), metric.Operation, metric.Kind, strconv.Itoa(metric.Status), fmt.Sprintf("%.3f", metric.ElapsedMS), strconv.FormatBool(metric.Expected429), strconv.FormatBool(metric.Timeout), metric.Error})
	}
	httpWriter.Flush()
	if err := httpWriter.Error(); err != nil {
		return err
	}

	taskFile, err := os.Create(filepath.Join(directory, "tasks.csv"))
	if err != nil {
		return err
	}
	defer taskFile.Close()
	taskWriter := csv.NewWriter(taskFile)
	_ = taskWriter.Write([]string{"timestamp", "job_id", "resource_id", "kind", "task_type", "status", "attempts", "asset_id", "created_at", "started_at", "finished_at", "error"})
	for _, metric := range recorder.tasks {
		_ = taskWriter.Write([]string{metric.Timestamp.Format(time.RFC3339Nano), metric.JobID, metric.ResourceID, metric.Kind, metric.TaskType, metric.Status, strconv.Itoa(metric.Attempts), metric.AssetID, metric.CreatedAt, metric.StartedAt, metric.FinishedAt, metric.Error})
	}
	taskWriter.Flush()
	if err := taskWriter.Error(); err != nil {
		return err
	}

	loadMetrics := filterHTTPMetrics(recorder.http, started, loadFinished)
	drainMetrics := filterHTTPMetrics(recorder.http, loadFinished, finished)
	loadSeconds := loadFinished.Sub(started).Seconds()
	acceptedRate := 0.0
	if loadSeconds > 0 {
		acceptedRate = float64(recorder.accepted) / loadSeconds
	}
	summary := map[string]any{
		"started_at":                 started.UTC().Format(time.RFC3339Nano),
		"load_finished_at":           loadFinished.UTC().Format(time.RFC3339Nano),
		"finished_at":                finished.UTC().Format(time.RFC3339Nano),
		"duration_seconds":           loadSeconds,
		"requested_duration_seconds": duration.Seconds(),
		"drain_seconds":              finished.Sub(loadFinished).Seconds(),
		"arrival_rate":               rate,
		"accepted_task_count":        recorder.accepted,
		"accepted_task_rate":         acceptedRate,
		"http_by_operation":          summarizeHTTP(loadMetrics),
		"http_drain_by_operation":    summarizeHTTP(drainMetrics),
		"http_total":                 summarizeHTTPTotal(loadMetrics, loadSeconds),
		"http_drain_total":           summarizeHTTPTotal(drainMetrics, finished.Sub(loadFinished).Seconds()),
		"task_count":                 len(recorder.tasks),
		"resource_count":             len(recorder.resource),
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "summary.json"), append(data, '\n'), 0o600)
}

// main 运行固定时长的混合资料负载，并输出可复查的原始 CSV 和汇总 JSON。
func main() {
	baseURLFlag := flag.String("base-url", "http://localhost:18082", "压测 Web 入口")
	originFlag := flag.String("origin", "", "写请求 Origin，默认使用 base-url")
	usernameFlag := flag.String("username", "", "压测账号用户名")
	fixtureDirFlag := flag.String("fixture-dir", "", "包含 sample.txt、sample.pdf、sample.png 的目录")
	outputDirFlag := flag.String("output-dir", "", "结果目录")
	durationFlag := flag.Duration("duration", time.Minute, "持续提交时长，例如 5m")
	rateFlag := flag.Float64("arrival-rate", 0.2, "每秒提交的任务数")
	readWorkersFlag := flag.Int("read-workers", 4, "并发读请求客户端数")
	flag.Parse()

	password := os.Getenv("MIZUKI_BENCH_PASSWORD")
	if *usernameFlag == "" || password == "" {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "必须提供 -username，并设置 MIZUKI_BENCH_PASSWORD")
		os.Exit(2)
	}
	if *durationFlag <= 0 || *rateFlag <= 0 || *readWorkersFlag < 1 {
		fmt.Fprintln(os.Stderr, "duration、arrival-rate 和 read-workers 必须为正数")
		os.Exit(2)
	}
	baseURL, err := normalizeBaseURL(*baseURLFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	origin := *originFlag
	if origin == "" {
		origin = baseURL
	}
	fixtureDir := *fixtureDirFlag
	if fixtureDir == "" {
		fixtureDir = filepath.Join(os.TempDir(), "mizuki-bench-fixtures")
	}
	outputDir := *outputDirFlag
	if outputDir == "" {
		outputDir = filepath.Join(os.TempDir(), "mizuki-bench-results", time.Now().Format("20060102-150405"))
	}
	fixtures, err := loadFixtures(fixtureDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	signalCtx, stopSignal := signalContext()
	defer stopSignal()
	loadCtx, cancelLoad := context.WithTimeout(signalCtx, *durationFlag)
	defer cancelLoad()
	pollCtx, cancelPoll := context.WithTimeout(signalCtx, *durationFlag+10*time.Minute)
	defer cancelPoll()
	recorder := &recorder{}
	started := time.Now()
	submitClient, err := newHTTPClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var workerWG sync.WaitGroup
	var pollWG sync.WaitGroup
	pollRequests := make(chan pollRequest, 256)
	const pollWorkers = 32
	pollWG.Add(pollWorkers)
	for index := 0; index < pollWorkers; index++ {
		go pollLoop(pollCtx, pollRequests, &pollWG)
	}
	workerWG.Add(1)
	go func() {
		defer workerWG.Done()
		submitLoop(loadCtx, pollCtx, pollRequests, submitClient, baseURL, origin, *usernameFlag, password, fixtures, *rateFlag, *durationFlag, recorder)
	}()
	for index := 0; index < *readWorkersFlag; index++ {
		client, clientErr := newHTTPClient()
		if clientErr != nil {
			fmt.Fprintln(os.Stderr, clientErr)
			os.Exit(1)
		}
		workerWG.Add(1)
		go func(workerID int, readClient *http.Client) {
			defer workerWG.Done()
			readLoop(loadCtx, readClient, baseURL, origin, *usernameFlag, password, workerID, recorder)
		}(index, client)
	}
	workerWG.Wait()
	loadFinished := time.Now()
	close(pollRequests)
	pollWG.Wait()
	finished := time.Now()
	if err := writeResults(outputDir, recorder, started, loadFinished, finished, *rateFlag, *durationFlag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("混合负载完成：结果目录 %s，HTTP 样本 %d，任务样本 %d\n", outputDir, len(recorder.http), len(recorder.tasks))
}

// signalContext 将 Ctrl+C 或进程终止信号转换为可传递给所有请求和轮询器的取消信号。
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
