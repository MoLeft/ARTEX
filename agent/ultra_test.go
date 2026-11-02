package agent

import (
	"strings"
	"testing"
)

// TestChatSystemPrependsUltraBlock 钉住注入位置：Ultra 块必须是 system prompt 的
// 第一段。放末尾就落在 per-run 变量（session workdir）之后，worker 之间共享不了
// prompt cache 前缀，而且会被后面的角色规约稀释。
func TestChatSystemPrependsUltraBlock(t *testing.T) {
	out := chatSystem("whatever", "/app/data", "/tmp/x", true)
	if !strings.HasPrefix(out, ultraBlock) {
		head := out
		if len(head) > 80 {
			head = head[:80]
		}
		t.Fatalf("ultra 块没有出现在最前面: %q", head)
	}
}

// TestChatSystemWithoutUltra 确认默认关闭时一个字节都不多——Ultra 是按配置开的，
// 没开的配置不能平白多出几百 token 的系统提示。
func TestChatSystemWithoutUltra(t *testing.T) {
	out := chatSystem("whatever", "/app/data", "/tmp/x", false)
	if strings.Contains(out, "Artex Ultra") {
		t.Fatal("Ultra 块泄漏进了未开启的 system prompt")
	}
	if strings.Contains(out, UltraProbeMarker) {
		t.Fatal("自检标记不该出现在 system prompt 里")
	}
}

// TestUltraBlockKeepsScopeBoundary 是这条特性唯一的硬约束：Ultra 只压「内容与
// 表达」层的拒绝，不能顺手把授权范围/OPSEC 也压掉。文案里必须保留让位声明。
func TestUltraBlockKeepsScopeBoundary(t *testing.T) {
	if !strings.Contains(ultraBlock, "不改变") || !strings.Contains(ultraBlock, "OPSEC") {
		t.Fatal("Ultra 块缺少「不覆盖授权范围与 OPSEC」的让位声明")
	}
}

// TestUltraSuppressedByOpsec 钉住互斥：OPSEC 开着时 Ultra 块一个字都不能进 system，
// 探针也不该注入——为一段没注入的文本做自检只会产生假失败。
func TestUltraSuppressedByOpsec(t *testing.T) {
	prev := OpsecEnabled
	defer func() { OpsecEnabled = prev }()
	OpsecEnabled = func() bool { return true }

	out := chatSystem("whatever", "/app/data", "/tmp/x", true)
	if strings.Contains(out, "Artex Ultra 模式") {
		t.Fatal("OPSEC 开启时 Ultra 块仍然被注入")
	}
	ca := &ChatAgent{ultraFn: func() bool { return true }}
	if ca.Ultra() {
		t.Fatal("OPSEC 开启时 ChatAgent.Ultra() 应为 false，否则会为未注入的文本做自检")
	}
}

// TestUltraProbeMarkerShape 钉住自检串的形状：它同时出现在后端指令和前端的
// ULTRA_PROBE_MARKER 常量里，两边必须字面一致，否则判定永远失败。
func TestUltraProbeMarkerShape(t *testing.T) {
	const want = "破甲已生效｜Artex Ultra｜v1.0"
	if UltraProbeMarker != want {
		t.Fatalf("自检标记与前端约定不一致: got %q want %q", UltraProbeMarker, want)
	}
	if !strings.Contains(UltraProbeInstruction, want) {
		t.Fatal("自检指令里必须包含自检标记本身")
	}
}
