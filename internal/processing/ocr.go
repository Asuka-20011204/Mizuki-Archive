package processing

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
	"mizuki-archive/internal/model"
)

const (
	// MaxOCRPages 限制单次 OCR 的页数，避免用户用超长 PDF 长时间占满 Worker。
	MaxOCRPages = 30
	// OCRRenderMaxDimension 限制 PDF 页面栅格化的最长边，平衡中文识别质量和内存占用。
	OCRRenderMaxDimension = 2200
	// OCRMaxOutputBytes 限制 OCR 派生正文大小，与普通文本提取共用同一安全边界。
	OCRMaxOutputBytes int64 = 10 << 20
	// OCRCommandTimeout 限制单页外部命令执行时间，父级 Worker 仍有更短的总任务超时。
	OCRCommandTimeout = 45 * time.Second
)

var (
	// ErrOCRUnavailable 表示运行环境没有可用的 Poppler 或 Tesseract 命令。
	ErrOCRUnavailable = errors.New("OCR tools unavailable")
	// ErrOCRInvalidSource 表示原件不是可读的图片/PDF，或 PDF 页数超过边界。
	ErrOCRInvalidSource = errors.New("invalid OCR source")
	// ErrOCRInvalidOutput 表示外部 OCR 输出不符合受控 TSV 格式。
	ErrOCRInvalidOutput = errors.New("invalid OCR output")
	// ErrOCREmpty 表示识别流程完成但没有找到可供检索的文字，不生成空的派生产物。
	ErrOCREmpty = errors.New("OCR found no text")
)

// OCRPageResult 保存单页识别正文、词数和 Tesseract 词级置信度平均值。
type OCRPageResult struct {
	Page       int
	Text       string
	Words      int
	Confidence float64
}

// OCRResult 保存 OCR 全文及每页质量摘要；置信度只是识别器指标，不是内容正确性证明。
type OCRResult struct {
	Text              string
	Pages             []OCRPageResult
	AverageConfidence float64
}

// RecognizeText 为图片或扫描 PDF 生成 UTF-8 OCR 正文；外部命令只处理受控临时文件，不执行用户内容。
func RecognizeText(ctx context.Context, resource model.Resource, sourcePath string) (OCRResult, error) {
	if err := ctx.Err(); err != nil {
		return OCRResult{}, err
	}
	if resource.Kind != "image" && resource.Kind != "pdf" {
		return OCRResult{}, ErrUnsupportedType
	}
	if err := validateOCRSource(sourcePath); err != nil {
		return OCRResult{}, err
	}
	if resource.Kind == "image" {
		if err := validateOCRImage(sourcePath); err != nil {
			return OCRResult{}, err
		}
		page, err := recognizeOCRImage(ctx, sourcePath, 1)
		if err != nil {
			return OCRResult{}, err
		}
		return combineOCRPages([]OCRPageResult{page})
	}
	return recognizeOCRPDF(ctx, sourcePath)
}

// validateOCRImage 先检查解码尺寸再交给外部进程，沿用缩略图的像素安全上限。
func validateOCRImage(sourcePath string) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return ErrOCRInvalidSource
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return ErrOCRInvalidSource
	}
	return validateImageBounds(config)
}

// validateOCRSource 只接受服务端已经登记的普通文件，避免外部命令读取目录或设备路径。
func validateOCRSource(sourcePath string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOCRInvalidSource, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return ErrOCRInvalidSource
	}
	return nil
}

// recognizeOCRPDF 先读取页数，再逐页栅格化和识别，避免一次性生成整本 PDF 的图片。
func recognizeOCRPDF(ctx context.Context, sourcePath string) (OCRResult, error) {
	pages, err := pdfPageCount(ctx, sourcePath)
	if err != nil {
		return OCRResult{}, err
	}
	if pages < 1 || pages > MaxOCRPages {
		return OCRResult{}, fmt.Errorf("%w: page count %d", ErrOCRInvalidSource, pages)
	}
	temporaryDir, err := os.MkdirTemp("", "mizuki-ocr-")
	if err != nil {
		return OCRResult{}, fmt.Errorf("create OCR temporary directory: %w", err)
	}
	defer os.RemoveAll(temporaryDir)
	results := make([]OCRPageResult, 0, pages)
	for page := 1; page <= pages; page++ {
		if err := ctx.Err(); err != nil {
			return OCRResult{}, err
		}
		imagePath, renderErr := renderPDFPage(ctx, sourcePath, temporaryDir, page)
		if renderErr != nil {
			return OCRResult{}, renderErr
		}
		pageResult, recognizeErr := recognizeOCRImage(ctx, imagePath, page)
		// 单页识别完成即删除渲染图，避免三十页图片同时占满容器的临时目录。
		removeErr := os.Remove(imagePath)
		if recognizeErr != nil {
			return OCRResult{}, recognizeErr
		}
		if removeErr != nil {
			return OCRResult{}, fmt.Errorf("remove OCR page image: %w", removeErr)
		}
		results = append(results, pageResult)
	}
	return combineOCRPages(results)
}

// pdfPageCount 使用 pdfinfo 读取结构信息，不解析 PDF 内的脚本或外部引用。
func pdfPageCount(ctx context.Context, sourcePath string) (int, error) {
	output, err := runOCRCommand(ctx, "pdfinfo", sourcePath)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(key), "Pages") {
			pages, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if parseErr != nil {
				return 0, fmt.Errorf("%w: invalid PDF page count", ErrOCRInvalidSource)
			}
			return pages, nil
		}
	}
	return 0, fmt.Errorf("%w: PDF page count missing", ErrOCRInvalidSource)
}

// renderPDFPage 将单页 PDF 渲染到临时 PNG，固定最长边以限制解码内存。
func renderPDFPage(ctx context.Context, sourcePath, temporaryDir string, page int) (string, error) {
	prefix := filepath.Join(temporaryDir, fmt.Sprintf("page-%03d", page))
	if _, err := runOCRCommand(ctx, "pdftoppm", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-singlefile", "-png", "-scale-to", strconv.Itoa(OCRRenderMaxDimension), sourcePath, prefix); err != nil {
		return "", err
	}
	imagePath := prefix + ".png"
	if err := validateOCRSource(imagePath); err != nil {
		return "", err
	}
	return imagePath, nil
}

// recognizeOCRImage 调用 Tesseract TSV 输出，保留词级置信度而不把原始诊断日志写入资料正文。
func recognizeOCRImage(ctx context.Context, imagePath string, page int) (OCRPageResult, error) {
	output, err := runOCRCommand(ctx, "tesseract", imagePath, "stdout", "-l", "chi_sim+eng", "--psm", "3", "tsv")
	if err != nil {
		return OCRPageResult{}, err
	}
	return parseOCRWords(output, page)
}

// runOCRCommand 只允许固定命令名并通过 exec 参数数组传递路径，防止 shell 注入和参数拼接。
func runOCRCommand(parent context.Context, name string, arguments ...string) ([]byte, error) {
	if name != "pdfinfo" && name != "pdftoppm" && name != "tesseract" {
		return nil, ErrOCRUnavailable
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, ErrOCRUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, OCRCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, arguments...)
	command.Stderr = io.Discard
	if name == "tesseract" {
		command.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	}
	// Tesseract TSV 附带坐标与置信度，原始输出大小不得随图片内容无界增长。
	output := &limitedOCROutput{limit: OCRMaxOutputBytes * 4}
	command.Stdout = output
	err = command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, context.DeadlineExceeded
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, context.Canceled
	}
	if err != nil {
		if output.exceeded {
			return nil, ErrOutputTooLarge
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrOCRUnavailable
		}
		return nil, fmt.Errorf("OCR command failed: %w", err)
	}
	return output.data.Bytes(), nil
}

// limitedOCROutput 在子进程写出超额数据时立即拒绝，避免命令输出无限占用 Worker 内存。
type limitedOCROutput struct {
	data     bytes.Buffer
	limit    int64
	exceeded bool
}

// Write 是命令的标准输出接收器，不保存超过限制的用户文件派生内容。
func (output *limitedOCROutput) Write(content []byte) (int, error) {
	if int64(output.data.Len()+len(content)) > output.limit {
		output.exceeded = true
		return 0, ErrOutputTooLarge
	}
	return output.data.Write(content)
}

// parseOCRWords 解析 Tesseract TSV 的词级行，忽略页眉、空词和负置信度。
func parseOCRWords(output []byte, page int) (OCRPageResult, error) {
	if len(output) == 0 || page < 1 {
		return OCRPageResult{}, ErrOCRInvalidOutput
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var texts []string
	var confidenceTotal float64
	var words int
	seenHeader := false
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 12)
		if len(fields) == 0 || fields[0] == "level" {
			seenHeader = true
			continue
		}
		if len(fields) < 12 || !seenHeader {
			return OCRPageResult{}, ErrOCRInvalidOutput
		}
		pageNumber, err := strconv.Atoi(fields[1])
		if err != nil {
			return OCRPageResult{}, ErrOCRInvalidOutput
		}
		// Tesseract 每次只接收一张渲染图，其 TSV 页码始终从 1 开始。
		if pageNumber != 1 {
			continue
		}
		confidence, err := strconv.ParseFloat(fields[10], 64)
		if err != nil {
			return OCRPageResult{}, ErrOCRInvalidOutput
		}
		text := strings.TrimSpace(fields[11])
		if text == "" || confidence < 0 {
			continue
		}
		texts = append(texts, text)
		confidenceTotal += confidence
		words++
	}
	if err := scanner.Err(); err != nil {
		return OCRPageResult{}, fmt.Errorf("read OCR output: %w", err)
	}
	if !seenHeader {
		return OCRPageResult{}, ErrOCRInvalidOutput
	}
	confidence := 0.0
	if words > 0 {
		confidence = math.Round(confidenceTotal/float64(words)*100) / 100
	}
	return OCRPageResult{Page: page, Text: strings.Join(texts, " "), Words: words, Confidence: confidence}, nil
}

// combineOCRPages 合并页面正文并计算按识别词数加权的平均置信度。
func combineOCRPages(pages []OCRPageResult) (OCRResult, error) {
	if len(pages) == 0 {
		return OCRResult{}, ErrOCRInvalidOutput
	}
	var builder strings.Builder
	var confidenceTotal float64
	var wordTotal int
	for _, page := range pages {
		if page.Text != "" {
			segment := page.Text
			if len(pages) > 1 {
				segment = fmt.Sprintf("第 %d 页（置信度 %.1f，仅供参考）\n%s", page.Page, page.Confidence, page.Text)
			}
			separator := ""
			if builder.Len() > 0 {
				separator = "\n\n"
			}
			if int64(builder.Len()+len(separator)+len(segment)) > OCRMaxOutputBytes {
				return OCRResult{}, ErrOutputTooLarge
			}
			builder.WriteString(separator + segment)
		}
		confidenceTotal += page.Confidence * float64(page.Words)
		wordTotal += page.Words
	}
	if wordTotal == 0 {
		return OCRResult{}, ErrOCREmpty
	}
	average := 0.0
	if wordTotal > 0 {
		average = math.Round(confidenceTotal/float64(wordTotal)*100) / 100
	}
	return OCRResult{Text: builder.String(), Pages: pages, AverageConfidence: average}, nil
}
