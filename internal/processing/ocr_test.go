package processing

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
)

// TestParseOCRWords 验证只索引文字层、忽略空格与负置信度，并保留页面边界。
func TestParseOCRWords(t *testing.T) {
	input := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"4\t1\t1\t1\t1\t0\t0\t0\t1\t1\t-1\t",
		"5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t87.5\tArchive",
		"5\t1\t1\t1\t1\t2\t0\t0\t1\t1\t62.5\tsearch",
		"5\t2\t1\t1\t1\t1\t0\t0\t1\t1\t35\t下一页",
	}, "\n")
	pageOne, err := parseOCRWords([]byte(input), 1)
	if err != nil || string(pageOne.Text) != "Archive search" || pageOne.Words != 2 || pageOne.Confidence != 75 {
		t.Fatalf("第一页 = %#v, %v", pageOne, err)
	}
	secondTSV := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t35\t下一页",
	}, "\n")
	pageTwo, err := parseOCRWords([]byte(secondTSV), 2)
	if err != nil || string(pageTwo.Text) != "下一页" || pageTwo.Words != 1 || pageTwo.Confidence != 35 {
		t.Fatalf("第二页 = %#v, %v", pageTwo, err)
	}
}

// TestOCRRecognizesSyntheticImage 用纯白背景上的合成字样测试外部引擎，不接触私人原件。
func TestOCRRecognizesSyntheticImage(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("未安装 Tesseract，跳过真实识别用例")
	}
	path := filepath.Join(t.TempDir(), "synthetic.png")
	small := image.NewRGBA(image.Rect(0, 0, 230, 60))
	for pixel := range small.Pix {
		small.Pix[pixel] = 255
	}
	face := font.Drawer{Dst: small, Src: image.NewUniform(color.Black), Face: basicfont.Face7x13, Dot: fixed.P(20, 34)}
	face.DrawString("ARCHIVE 123")
	large := image.NewRGBA(image.Rect(0, 0, 920, 240))
	draw.NearestNeighbor.Scale(large, large.Bounds(), small, small.Bounds(), draw.Src, nil)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, large); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := RecognizeText(context.Background(), model.Resource{Kind: "image"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(result.Text), "ARCHIVE") || result.AverageConfidence <= 0 || len(result.Pages) != 1 {
		t.Fatalf("合成图片 OCR 结果异常: %#v", result)
	}
}

// TestOCRRecognizesTwoScannedPDFPages 验证 PDF 栅格化逐页运行且第二页 TSV 页码重置后仍正确归属。
func TestOCRRecognizesTwoScannedPDFPages(t *testing.T) {
	for _, tool := range []string{"tesseract", "pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("缺少 OCR 工具: " + tool)
		}
	}
	small := image.NewRGBA(image.Rect(0, 0, 230, 60))
	for pixel := range small.Pix {
		small.Pix[pixel] = 255
	}
	face := font.Drawer{Dst: small, Src: image.NewUniform(color.Black), Face: basicfont.Face7x13, Dot: fixed.P(20, 34)}
	face.DrawString("ARCHIVE 123")
	large := image.NewRGBA(image.Rect(0, 0, 920, 240))
	draw.NearestNeighbor.Scale(large, large.Bounds(), small, small.Bounds(), draw.Src, nil)
	path := filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(path, twoPageScannedPDF(large), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := RecognizeText(context.Background(), model.Resource{Kind: "pdf"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 2 || result.Pages[1].Page != 2 || !strings.Contains(result.Text, "第 2 页") || result.Pages[0].Text == "" || result.Pages[1].Text != result.Pages[0].Text {
		t.Fatalf("第二页识别没有绑定 PDF 原页码: 页数=%d，第二页文本是否与第一页一致=%t", len(result.Pages), len(result.Pages) == 2 && result.Pages[1].Text == result.Pages[0].Text)
	}
}

// twoPageScannedPDF 用合成像素构造纯图片 PDF，两页共享同一图像且没有可提取文本层。
func twoPageScannedPDF(source *image.RGBA) []byte {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	for position := source.Bounds().Min.Y; position < source.Bounds().Max.Y; position++ {
		for column := source.Bounds().Min.X; column < source.Bounds().Max.X; column++ {
			pixel := source.RGBAAt(column, position)
			_, _ = writer.Write([]byte{pixel.R, pixel.G, pixel.B})
		}
	}
	_ = writer.Close()
	content := []byte("q\n920 0 0 240 20 20 cm\n/Im1 Do\nQ\n")
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 960 280] /Contents 5 0 R /Resources << /XObject << /Im1 6 0 R >> >> >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 960 280] /Contents 5 0 R /Resources << /XObject << /Im1 6 0 R >> >> >>"),
		append([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n", len(content))), append(content, []byte("\nendstream")...)...),
		append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 920 /Height 240 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", compressed.Len())), append(compressed.Bytes(), []byte("\nendstream")...)...),
	}
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n", index+1)
		output.Write(object)
		output.WriteString("\nendobj\n")
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return output.Bytes()
}

// TestOCRRejectsInvalidInput 验证空内容、畸形 TSV、超量页数和不允许的资料类型均安全失败。
func TestOCRRejectsInvalidInput(t *testing.T) {
	for _, content := range []string{"", "level\tpage_num\tconf\ttext\n5\t1\tnot-a-number\tword", "<script>html</script>"} {
		if _, err := parseOCRWords([]byte(content), 1); !errors.Is(err, ErrOCRInvalidOutput) {
			t.Fatalf("输出 %q 应拒绝，得到 %v", content, err)
		}
	}
	if _, err := RecognizeText(context.Background(), model.Resource{Kind: "text"}, "unused"); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("未授权的格式 = %v", err)
	}
	if _, err := RecognizeText(context.Background(), model.Resource{Kind: "pdf"}, "unused"); !errors.Is(err, ErrOCRUnavailable) && !errors.Is(err, ErrOCRInvalidSource) {
		t.Fatalf("不存在的来源不应成功: %v", err)
	}
}

// TestOCRRespectsContext 确保取消后不运行 PDF 解析或外部 OCR 命令。
func TestOCRRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RecognizeText(ctx, model.Resource{Kind: "image"}, "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后仍执行: %v", err)
	}
}

// TestOCRPDFPageLimit 用可控合成 PDF 验证页数上限而不读取个人资料。
func TestOCRPDFPageLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4\nnot a PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecognizeText(context.Background(), model.Resource{Kind: "pdf"}, path); err == nil {
		t.Fatal("畸形 PDF 不应产生 OCR 成果")
	}
}
