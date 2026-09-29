package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// TestV7MixedProcessingWorkload 使用四页 PDF 和四张九百万像素图片检查混合处理与内存边界。
func TestV7MixedProcessingWorkload(t *testing.T) {
	if os.Getenv("MIZUKI_RUN_BENCHMARK") != "1" {
		t.Skip("显式设置 MIZUKI_RUN_BENCHMARK=1 才运行混合负载")
	}
	ctx, store, database := openV7BenchmarkDatabase(t)
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
			processor, jobIDs := seedV7MixedFixture(t, ctx, store, database)
			measureV7Processing(t, ctx, database, processor, jobIDs, workers, "pdf4_png4_3000px")
		})
	}
}

// seedV7MixedFixture 在临时目录造真实可处理文件，只清理本轮生成的数据库资源和派生物。
func seedV7MixedFixture(t *testing.T, ctx context.Context, store *repository.MySQL, database *gorm.DB) (*Processing, []string) {
	t.Helper()
	dataDir := t.TempDir()
	processor, err := NewProcessing(store, store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	imageContent := v7LargePNG(t)
	pdfContent := v7TextPDF("mizuki mixed synthetic keyword")
	resourceIDs := make([]string, 0, 8)
	jobIDs := make([]string, 0, 8)
	t.Cleanup(func() {
		if len(resourceIDs) != 0 {
			_ = database.Exec("DELETE FROM resources WHERE id IN ?", resourceIDs).Error
		}
	})
	for index := 0; index < 8; index++ {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			t.Fatal(err)
		}
		resourceID := hex.EncodeToString(idBytes)
		content, kind, mimeType, extension := pdfContent, "pdf", "application/pdf", "pdf"
		if index%2 == 1 {
			content, kind, mimeType, extension = imageContent, "image", "image/png", "png"
		}
		if err := os.WriteFile(filepath.Join(dataDir, resourceID), content, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		name := fmt.Sprintf("mixed-%02d.%s", index, extension)
		resource := model.Resource{ID: resourceID, Name: name, OriginalName: name, Kind: kind, MIME: mimeType, Size: int64(len(content)), StorageKey: resourceID, SHA256: hex.EncodeToString(digest[:]), CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
		resourceIDs = append(resourceIDs, resourceID)
		var job model.ProcessingJob
		if kind == "pdf" {
			job, err = processor.CreateTextJob(ctx, resourceID)
		} else {
			job, err = processor.CreateThumbnailJob(ctx, resourceID)
		}
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, job.ID)
	}
	return processor, jobIDs
}

// v7LargePNG 构造受当前图片限制允许的 3000×3000 PNG，不从仓库或个人文件读取。
func v7LargePNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 3000, 3000))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// v7TextPDF 构造带可提取文本的一页 PDF，交叉引用偏移由实际写入长度计算。
func v7TextPDF(text string) []byte {
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
