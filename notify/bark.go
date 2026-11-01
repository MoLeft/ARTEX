package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

type barkChannel struct{}

func (barkChannel) Kind() string { return KindBark }

func (barkChannel) DefaultRatePerMin() int { return 0 }

func (barkChannel) SecretKeys() []string { return []string{"device_key"} }

func (barkChannel) DestinationKeys() []string { return []string{"base_url"} }

func (barkChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "device_key") == "" {
		return errors.New("缺少 Bark 设备 Key")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("Bark 服务地址无效: %w", err)
		}
		u, _ := url.Parse(base)
		if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return errors.New("Bark 服务地址不能包含账号、查询参数或片段")
		}
	}
	for _, key := range []string{"icon", "click_url"} {
		if value := cfgString(cfg, key); value != "" {
			if err := validateHTTPURL(value); err != nil {
				return fmt.Errorf("%s 无效: %w", key, err)
			}
		}
	}
	switch cfgString(cfg, "level") {
	case "", "active", "timeSensitive", "passive", "critical":
	default:
		return errors.New("Bark 提醒级别无效")
	}
	switch cfgString(cfg, "action") {
	case "", "open", "none":
	default:
		return errors.New("Bark 点击行为无效")
	}
	for _, key := range []string{"badge", "volume"} {
		switch value := cfg[key].(type) {
		case nil, int:
		case float64:
			if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
				return fmt.Errorf("Bark %s 必须是整数", key)
			}
		case string:
			if value != "" {
				if _, err := strconv.Atoi(strings.TrimSpace(value)); err != nil {
					return fmt.Errorf("Bark %s 必须是整数", key)
				}
			}
		default:
			return fmt.Errorf("Bark %s 必须是整数", key)
		}
	}
	if cfgInt(cfg, "badge") < 0 || cfgInt(cfg, "volume") < 0 || cfgInt(cfg, "volume") > 10 {
		return errors.New("Bark 角标不能为负，重要警告音量须为 0–10")
	}
	return nil
}

func (c barkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.day.app"
	}
	base = strings.TrimRight(base, "/")
	body, kept := barkBody(m)
	if kept == 0 {
		return 0, Permanent(errors.New("Bark 消息没有可发送的漏洞"))
	}
	title := markdownTitle(m)
	if m.Batch && kept < len(m.Items) {
		title = fmt.Sprintf("漏洞汇总 · 共 %d 条（本次 %d 条）", len(m.Items), kept)
	}
	if prefix := cfgString(cfg, "title_prefix"); prefix != "" {
		title = prefix + " · " + title
	}
	payload := map[string]any{
		"device_key": cfgString(cfg, "device_key"),
		"title":      TruncateRunes(title, 150),
		"body":       body,
	}
	for _, key := range []string{"subtitle", "icon", "sound", "group", "level"} {
		if value := cfgString(cfg, key); value != "" {
			payload[key] = value
		}
	}
	if _, custom := payload["icon"]; !custom && m.HomeURL != "" {
		if u, err := url.Parse(m.HomeURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			u.Path = strings.TrimRight(u.Path, "/") + "/logo.png"
			u.RawQuery, u.Fragment = "", ""
			payload["icon"] = u.String()
		}
	}
	if n := cfgInt(cfg, "badge"); n > 0 {
		payload["badge"] = n
	}
	if value, ok := cfg["volume"]; ok && value != "" && value != nil {
		payload["volume"] = cfgInt(cfg, "volume")
	}
	if cfgBool(cfg, "call") {
		payload["call"] = "1"
	}
	if cfgBool(cfg, "is_archive") {
		payload["isArchive"] = "1"
	}
	if cfgString(cfg, "action") == "none" {
		payload["action"] = "none"
	} else {
		link := cfgString(cfg, "click_url")
		if link == "" && m.Batch {
			link = m.HomeURL
		} else if link == "" {
			link = m.Items[0].DetailURL
		}
		if link != "" {
			payload["url"] = link
		}
	}
	raw, err := doJSON(ctx, "POST", base+"/push", nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("解析 Bark 响应失败: %w (%s)", err, snippet(raw))
	}
	if res.Code == 200 {
		return kept, nil
	}
	if res.Code == 429 {
		return 0, fmt.Errorf("Bark 限流: %s", res.Message)
	}
	return 0, Permanent(fmt.Errorf("Bark 返回错误 %d: %s", res.Code, res.Message))
}

func barkBody(m Message) (string, int) {
	if len(m.Items) == 0 {
		return "", 0
	}
	if !m.Batch {
		it := m.Items[0]
		var lines []string
		if it.IsStatusChange() {
			lines = append(lines, "状态："+StatusLabel(it.FromStatus)+" → "+StatusLabel(it.ToStatus))
		}
		if it.VulnClass != "" {
			lines = append(lines, "类型："+OneLine(it.VulnClass, 80))
		}
		if assets := assetLine(it.Assets, maxAssetsShown); assets != "" {
			lines = append(lines, "资产："+OneLine(assets, 160))
		}
		if summary := OneLine(it.Summary, 500); summary != "" {
			lines = append(lines, "摘要："+summary)
		}
		if len(lines) == 0 {
			lines = append(lines, it.Title())
		}
		return TruncateRunes(strings.Join(lines, "\n"), 3500), 1
	}
	line := func(it Item, idx int) string {
		return fmt.Sprintf("%d. %s · %s\n", idx+1, SeverityLabel(it.Severity), OneLine(it.Title(), 120))
	}
	kept := packItemCount(m.Items, 3500, 180, "", runeSize, line)
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "近 %d 分钟 · ", m.WindowMinutes)
	}
	if kept < len(m.Items) {
		fmt.Fprintf(&b, "共 %d 条漏洞，本次前 %d 条\n", len(m.Items), kept)
	} else {
		fmt.Fprintf(&b, "共 %d 条漏洞\n", len(m.Items))
	}
	for i, it := range m.Items[:kept] {
		b.WriteString(line(it, i))
	}
	return TruncateRunes(b.String(), 3500), kept
}
