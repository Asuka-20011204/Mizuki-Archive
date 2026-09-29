package processing

import (
	"context"
	"errors"
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
