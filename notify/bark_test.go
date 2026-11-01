package notify

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestBarkSendsCustomPushAndDefaultLinks(t *testing.T) {
	srv := capturePost(t, `{"code":200,"message":"success"}`, func(t *testing.T, body map[string]any, r *http.Request) {
		if r.URL.Path != "/push" || body["device_key"] != "device-secret" {
			t.Errorf("Bark endpoint/key 错误: %s %v", r.URL.Path, body["device_key"])
		}
		if body["icon"] != "https://example.com/logo.png" || body["sound"] != "alarm" || body["level"] != "timeSensitive" || body["call"] != "1" {
			t.Errorf("显示/声音配置丢失: %v", body)
		}
		if body["url"] != "https://artex.local/function/findings/detail?id=42" {
			t.Errorf("单条消息应默认打开漏洞详情: %v", body["url"])
		}
		if !strings.Contains(body["body"].(string), "参数 id") {
			t.Errorf("正文缺少漏洞摘要: %v", body["body"])
		}
	})
	cfg := map[string]any{"base_url": srv.URL, "device_key": "device-secret", "icon": "https://example.com/logo.png", "sound": "alarm", "level": "timeSensitive", "call": true}
	t.Setenv(AllowLocalTargetsEnv, "1")
	if kept, err := (barkChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil || kept != 1 {
		t.Fatalf("Bark 发送失败 kept=%d err=%v", kept, err)
	}
}

func TestBarkBatchCustomLinkAndFailure(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "1")
	srv := capturePost(t, `{"code":400,"message":"invalid device key"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["url"] != "https://example.com/dashboard" || body["title"] != "自定义 · 漏洞汇总 · 共 3 条" {
			t.Errorf("汇总标题/自定义链接错误: %v", body)
		}
	})
	cfg := map[string]any{"base_url": srv.URL, "device_key": "key", "click_url": "https://example.com/dashboard", "title_prefix": "自定义"}
	if _, err := (barkChannel{}).Send(context.Background(), cfg, batchMsg(3)); err == nil || !IsPermanent(err) {
		t.Fatalf("Bark HTTP 200 业务失败应为永久错误: %v", err)
	}
}

func TestBarkUsesARTEXLogoAndCanDisableTap(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "1")
	srv := capturePost(t, `{"code":200,"message":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["icon"] != "https://artex.local/logo.png" {
			t.Errorf("应自动使用 ARTEX Logo: %v", body["icon"])
		}
		if body["action"] != "none" || body["url"] != nil {
			t.Errorf("关闭点击行为时不应带跳转链接: %v", body)
		}
		if body["volume"] != float64(0) {
			t.Errorf("重要警告音量 0 应原样发送: %v", body["volume"])
		}
	})
	cfg := map[string]any{"base_url": srv.URL, "device_key": "key", "action": "none", "volume": 0}
	m := singleMsg()
	m.HomeURL = "https://artex.local"
	if _, err := (barkChannel{}).Send(context.Background(), cfg, m); err != nil {
		t.Fatalf("Bark 发送失败: %v", err)
	}
}

func TestBarkValidationAndDestinationProtection(t *testing.T) {
	ch := barkChannel{}
	for _, cfg := range []map[string]any{
		{},
		{"device_key": "key", "base_url": "file:///tmp/push"},
		{"device_key": "key", "icon": "javascript:alert(1)"},
		{"device_key": "key", "click_url": "javascript:alert(1)"},
		{"device_key": "key", "level": "urgent"},
		{"device_key": "key", "badge": -1},
		{"device_key": "key", "badge": 1.5},
		{"device_key": "key", "volume": "loud"},
	} {
		if err := ch.Validate(cfg); err == nil {
			t.Errorf("应拒绝配置: %v", cfg)
		}
	}
	stored := map[string]any{"base_url": "https://old.example.com", "device_key": "secret"}
	if masked := MaskConfig(KindBark, stored); masked["device_key"] == "secret" || masked["base_url"] != stored["base_url"] {
		t.Fatalf("Bark Key 应掩码且服务地址应保留: %v", masked)
	}
	incoming := map[string]any{"base_url": "https://new.example.com", "device_key": MaskedValue("secret")}
	if _, err := PrepareConfigUpdate(KindBark, stored, incoming); err == nil {
		t.Fatal("更改 Bark 服务器时应要求重新填写 Key")
	}
}

func TestBarkBatchKeepsOnlyRenderedItems(t *testing.T) {
	m := batchMsg(100)
	for i := range m.Items {
		m.Items[i].Name = strings.Repeat("漏洞", 100)
	}
	body, kept := barkBody(m)
	if kept <= 0 || kept >= len(m.Items) || !strings.Contains(body, "本次前 "+itoa(kept)+" 条") {
		t.Fatalf("汇总消息应分段并明确本次条数: kept=%d body=%q", kept, body)
	}
	if strings.Contains(body, itoa(kept+1)+". ") {
		t.Fatalf("正文不应包含未计入 kept 的条目: kept=%d", kept)
	}
}
