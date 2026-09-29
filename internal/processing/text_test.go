package processing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
)

// TestExtractTextReadsPlainText 验证 TXT 和 Markdown 原样读取并保留用户文本。
func TestExtractTextReadsPlainText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	content := []byte("标题\n<script>仍然只是文本</script>\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"text", "markdown"} {
		result, err := ExtractText(context.Background(), model.Resource{Kind: kind}, path)
		if err != nil || string(result) != string(content) {
			t.Fatalf("kind=%s result=%q error=%v", kind, result, err)
		}
	}
}

// TestExtractTextReadsPDF 验证 PDF 处理器能从合法文本页提取正文，而不是只覆盖纯文本文件。
func TestExtractTextReadsPDF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, minimalTextPDF("hello pdf keyword"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ExtractText(context.Background(), model.Resource{Kind: "pdf"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "hello pdf keyword") {
		t.Fatalf("PDF text = %q", result)
	}
}

// minimalTextPDF 构造一个带 Helvetica 文本页的最小 PDF，测试不依赖仓库外的个人文件。
func minimalTextPDF(text string) []byte {
	stream := fmt.Sprintf("BT\n/F1 24 Tf\n72 720 Td\n(%s) Tj\nET\n", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xrefOffset := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return output.Bytes()
}

// TestGenerateThumbnailResizesPNG 验证图片任务生成受控尺寸的 PNG，并且不改变原图。
func TestGenerateThumbnailResizesPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.png")
	source := image.NewRGBA(image.Rect(0, 0, 1200, 600))
	for y := 0; y < source.Bounds().Dy(); y++ {
		for x := 0; x < source.Bounds().Dx(); x++ {
			source.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, source); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := GenerateThumbnail(context.Background(), model.Resource{Kind: "image"}, path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 640 || decoded.Bounds().Dy() != 320 {
		t.Fatalf("thumbnail size = %v, want 640x320", decoded.Bounds())
	}
}

// TestGenerateThumbnailReadsJPEG 验证现有图片上传白名单中的 JPEG 也能进入缩略图处理器。
func TestGenerateThumbnailReadsJPEG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jpg")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	source := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < source.Bounds().Dy(); y++ {
		for x := 0; x < source.Bounds().Dx(); x++ {
			source.SetRGBA(x, y, color.RGBA{R: 80, G: uint8(x % 255), B: uint8(y % 255), A: 255})
		}
	}
	if err := jpeg.Encode(file, source, &jpeg.Options{Quality: 85}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := GenerateThumbnail(context.Background(), model.Resource{Kind: "image"}, path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 640 || decoded.Bounds().Dy() != 320 {
		t.Fatalf("thumbnail size = %v, want 640x320", decoded.Bounds())
	}
}

// TestGenerateThumbnailRejectsOversizedImage 验证解码前的像素边界能拒绝危险图片。
func TestGenerateThumbnailRejectsOversizedImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, ThumbnailMaxSourceDimension+1, 1))); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateThumbnail(context.Background(), model.Resource{Kind: "image"}, path); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversized image error = %v", err)
	}
}

// TestExtractTextRejectsUnsupportedAndInvalidUTF8 验证处理器白名单和文本编码边界。
func TestExtractTextRejectsUnsupportedAndInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractText(context.Background(), model.Resource{Kind: "image"}, path); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("unsupported type error = %v", err)
	}
	if _, err := ExtractText(context.Background(), model.Resource{Kind: "text"}, path); !errors.Is(err, ErrInvalidText) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
}

// TestExtractTextStopsAtOutputLimit 验证超过输出上限时不会继续返回完整内容。
func TestExtractTextStopsAtOutputLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", int(MaxExtractedTextBytes)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractText(context.Background(), model.Resource{Kind: "text"}, path); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("large output error = %v", err)
	}
}
