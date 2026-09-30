package model

import "testing"

// TestExternalLinkKey 验证大小写、默认端口、片段和查询顺序规范化，同时保留真实查询参数。
func TestExternalLinkKey(t *testing.T) {
	baseline := ExternalLinkKey("HTTPS://EXAMPLE.ORG:443/course?b=2&a=1#section")
	if baseline == "" || baseline != ExternalLinkKey("https://example.org/course?a=1&b=2") {
		t.Fatalf("equivalent links disagree: %q", baseline)
	}
	if baseline == ExternalLinkKey("https://example.org/course?a=1&b=3") || baseline == ExternalLinkKey("https://example.org/course/?a=1&b=2") {
		t.Fatal("distinct link content collapsed")
	}
	if ExternalLinkKey("https://example.org/a%2Fb") == ExternalLinkKey("https://example.org/a/b") {
		t.Fatal("encoded slash and path separator must remain distinct")
	}
	for _, input := range []string{"", "local-folder", "file:///etc/passwd", "javascript:alert(1)", "https://user:secret@example.org/course", "https://example.org/%zz", "https://example.org/?a=%zz", "http://./"} {
		if key := ExternalLinkKey(input); key != "" {
			t.Fatalf("ineligible link %q returned key %q", input, key)
		}
	}
}
