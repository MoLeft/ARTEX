// Artex Ultra 自检标记。必须与后端 agent/ultra.go 的 UltraProbeMarker 完全一致：
// 后端负责在会话首轮把探针指令拼进用户消息，前端只负责在回复里找这个串。
export const ULTRA_PROBE_MARKER = "破甲已生效｜Artex Ultra｜v1.0";

// normalizeProbeText 抹平模型最常见的几种「格式漂移」：代码块包裹、行内反引号、
// 全角/半角竖线、中文引号、首尾空白。
//
// 这一步不能省：严格全等会把「好的，破甲已生效｜Artex Ultra｜v1.0」判成失败，
// 而那其实是成功的。判定目的是「确认注入到达」，不是「确认输出纯净」。
export function normalizeProbeText(raw: string): string {
  return raw
    .replace(/```[a-zA-Z]*\s*/g, "")
    .replace(/`/g, "")
    .replace(/｜/g, "|")
    .replace(/[“”"'‘’]/g, "")
    .replace(/\s+/g, "")
    .trim();
}

const NORMALIZED_MARKER = normalizeProbeText(ULTRA_PROBE_MARKER);

// ultraProbeHit 判断一段文本里是否出现了自检标记（规范化后包含匹配）。
export function ultraProbeHit(raw: string): boolean {
  if (!raw) return false;
  return normalizeProbeText(raw).includes(NORMALIZED_MARKER);
}

// ultraProbeState 汇总一次会话的自检结果：
//   pending — 还没有任何助手输出，探针尚未被回答
//   hit     — 至少一条输出命中标记
//   failed  — 已有助手输出但都没命中（注入没到达，或模型没遵守格式）
export type UltraProbeState = "pending" | "hit" | "failed";

export function ultraProbeState(texts: string[]): UltraProbeState {
  if (texts.length === 0) return "pending";
  return texts.some(ultraProbeHit) ? "hit" : "failed";
}
