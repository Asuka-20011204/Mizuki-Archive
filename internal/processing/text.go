// Package processing 存放与 HTTP 无关的文件处理器，Worker 只通过这里读取和生成派生内容。
package processing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"mizuki-archive/internal/model"
)

// MaxExtractedTextBytes 限制单个派生文本的大小，避免畸形 PDF 造成无限增长的结果文件。
const MaxExtractedTextBytes int64 = 10 << 20

var (
	// ErrUnsupportedType 表示处理器不接受当前资料类型。
	ErrUnsupportedType = errors.New("unsupported text extraction type")
	// ErrInvalidText 表示文本不是合法 UTF-8，避免把不可预测字节写入检索索引。
	ErrInvalidText = errors.New("invalid UTF-8 text")
	// ErrOutputTooLarge 表示提取结果超过受控索引大小。
	ErrOutputTooLarge = errors.New("extracted text too large")
)

// ExtractText 从 PDF、TXT 或 Markdown 读取 UTF-8 文本；不会执行 Markdown 或任何嵌入脚本。
func ExtractText(ctx context.Context, resource model.Resource, sourcePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch resource.Kind {
	case "text", "markdown":
		return readPlainText(ctx, sourcePath)
	case "pdf":
		return readPDFText(ctx, sourcePath)
	default:
		return nil, ErrUnsupportedType
	}
}

// readPlainText 原样保留 TXT/Markdown 内容，只做大小和 UTF-8 校验。
func readPlainText(ctx context.Context, sourcePath string) ([]byte, error) {
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open text source: %w", err)
	}
	defer file.Close()
	return readLimitedUTF8(ctx, file)
}

// readPDFText 使用 PDF 阅读器提取文本，不把 PDF 内容当作 HTML 返回给浏览器。
func readPDFText(ctx context.Context, sourcePath string) ([]byte, error) {
	file, reader, err := pdf.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open PDF source: %w", err)
	}
	defer file.Close()
	plainText, err := reader.GetPlainText()
	if err != nil {
		return nil, fmt.Errorf("extract PDF text: %w", err)
	}
	return readLimitedUTF8(ctx, plainText)
}

// readLimitedUTF8 分块读取处理器输出，超过边界时立即停止而不是继续占用内存。
func readLimitedUTF8(ctx context.Context, source io.Reader) ([]byte, error) {
	var builder strings.Builder
	builder.Grow(32 * 1024)
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := source.Read(buffer)
		if count > 0 {
			if int64(builder.Len()+count) > MaxExtractedTextBytes {
				return nil, ErrOutputTooLarge
			}
			builder.Write(buffer[:count])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read extracted text: %w", err)
		}
	}
	content := builder.String()
	if !utf8.ValidString(content) {
		return nil, ErrInvalidText
	}
	return []byte(content), nil
}
