// 渠道字段表与配置值的解析工具。
//
// 与页面拆开是因为这一份是**数据**而不是视图：它描述每种渠道有哪些字段、
// 各自该用什么控件，以及表单文本到配置值（JSON）的双向转换。
// 单独放一个文件后，新增渠道只需要动这里，页面本身不必改。
// 渠道类型的展示名与简介。放在前端是因为它只影响文案，后端不需要知道。
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "钉钉",
  feishu: "飞书",
  wecom: "企业微信",
  webhook: "通用 Webhook",
  telegram: "Telegram",
  email: "邮件",
  bark: "Bark",
};

// 各渠道的配置字段定义。
//
// 这里刻意保留一份前端字段表，而不是让后端下发 schema：后端只负责
// Validate（必填/格式），UI 需要的是布局与控件类型，两者关注的不是同一件事。
// 唯一的耦合点是 secret_keys —— 哪些字段该渲染成密码框由后端给出，
// 因为只有渠道实现自己清楚哪些值算凭据（企业微信的整个 Webhook 就是凭据，
// 而钉钉的只是其中一个 secret）。新增渠道时这里少一个条目只会让表单变空白，
// 不会静默出错（下面的 hasFields 会提示）。
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Webhook 地址",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "加签密钥",
      kind: "password",
      help: "机器人安全设置选「加签」时填写；选「自定义关键词」或未开启安全设置则留空",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Webhook 地址",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "签名校验密钥", kind: "password", help: "机器人开启「签名校验」时填写，否则留空" },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Webhook 地址",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "目标 URL", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "请求方法",
      kind: "select",
      options: [
        { value: "POST", label: "POST（带请求体）" },
        { value: "PUT", label: "PUT（带请求体）" },
        { value: "PATCH", label: "PATCH（带请求体）" },
        { value: "GET", label: "GET（不带请求体）" },
      ],
    },
    { key: "headers", label: "自定义请求头", kind: "kv", help: "每行 KEY=VALUE，例如 Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "请求体模板",
      kind: "textarea",
      help:
        "留空用内置默认模板。变量：{{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}，" +
        "以及 range .Items 下的 .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel。" +
        "插入字符串请用 {{json .Xxx}} 而不是 {{.Xxx}}，否则标题里的引号会破坏 JSON。",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "API 地址",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "留空用官方地址；自建 Bot API 反代时填写",
    },
  ],
  bark: [
    { key: "device_key", label: "设备 Key", kind: "password", help: "从 Bark App 复制；保存后只显示尾号" },
    {
      key: "base_url",
      label: "Bark 服务地址",
      kind: "text",
      placeholder: "https://api.day.app",
      help: "留空使用 Bark 官方服务；自建服务填根地址，不含 Key 或 /push。本机地址需设置 ARTEX_NOTIFY_ALLOW_LOCAL=1",
    },
    {
      key: "title_prefix",
      label: "标题前缀",
      kind: "text",
      placeholder: "我的 ARTEX",
      help: "加在漏洞标题前；留空直接显示漏洞级别与名称",
    },
    { key: "subtitle", label: "副标题", kind: "text", placeholder: "安全告警", help: "可选，显示在通知标题与正文之间" },
    {
      key: "icon",
      label: "Logo 图片 URL",
      kind: "text",
      placeholder: "https://example.com/logo.png",
      help: "留空时使用全局回链地址下的 ARTEX Logo；自定义图标需手机可访问，支持 iOS 15 及以上",
    },
    {
      key: "sound",
      label: "铃声名称",
      kind: "text",
      placeholder: "alarm",
      help: "填写 Bark 内置或已导入的自定义铃声名，如 alarm、minuet；留空使用设备默认铃声",
    },
    {
      key: "level",
      label: "显示与提醒级别",
      kind: "select",
      help: "普通会亮屏；时效性可穿透专注模式；静默仅进入通知列表；重要警告需要 iOS 授权。震动由 iOS 的声音与触感设置控制，Bark API 无独立震动参数。",
      options: [
        { value: "active", label: "普通提醒（默认）" },
        { value: "timeSensitive", label: "时效性提醒" },
        { value: "passive", label: "静默显示" },
        { value: "critical", label: "重要警告" },
      ],
    },
    { key: "volume", label: "重要警告音量（0–10）", kind: "number", help: "仅在重要警告级别生效；留空由 Bark 决定" },
    { key: "call", label: "重复响铃 30 秒", kind: "switch", help: "仅用于确需持续提醒的推送" },
    { key: "group", label: "通知分组", kind: "text", placeholder: "ARTEX 漏洞", help: "在 Bark 历史记录中按组查看" },
    { key: "badge", label: "App 图标角标", kind: "number", help: "留空不设置；填写正整数会覆盖现有角标数字" },
    {
      key: "click_url",
      label: "点击跳转 URL",
      kind: "text",
      placeholder: "https://example.com/dashboard",
      help: "留空时单条跳转漏洞详情，汇总跳转漏洞列表；需先设置上方全局回链地址",
    },
    {
      key: "action",
      label: "点击行为",
      kind: "select",
      options: [
        { value: "open", label: "打开跳转链接" },
        { value: "none", label: "不执行操作" },
      ],
    },
    { key: "is_archive", label: "强制保存到 Bark 历史记录", kind: "switch", help: "关闭时不覆盖 Bark 自身的归档设置" },
  ],
  email: [
    { key: "host", label: "SMTP 服务器", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "端口",
      kind: "number",
      placeholder: "587",
      help: "587 走 STARTTLS；465 请把「隐式 TLS」打开",
    },
    { key: "username", label: "账号", kind: "text" },
    { key: "password", label: "密码 / 授权码", kind: "password" },
    { key: "from", label: "发件人", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "收件人", kind: "list", help: "多个地址用逗号分隔" },
    { key: "tls", label: "隐式 TLS", kind: "switch", help: "465 端口打开；587 保持关闭（会自动 STARTTLS）" },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "不限" },
  { value: "low", label: "低危及以上" },
  { value: "medium", label: "中危及以上" },
  { value: "high", label: "高危及以上" },
  { value: "critical", label: "仅严重" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV 解析「每行 KEY=VALUE」的文本域。
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs 解析逗号/空白分隔的 id 列表。
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords 解析行/逗号分隔的关键词列表（漏洞类型名可能含空格，所以按行或逗号切）。
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
