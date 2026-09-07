package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// 智谱 GLM Coding Plan 官方用量查询（与官方 glm-plan-usage 插件相同的数据源）：
// GET {domain}/api/monitor/usage/quota/limit
//   国内: https://open.bigmodel.cn   海外: https://api.z.ai
// 请求头 Authorization 直接携带 API Key（不带 Bearer 前缀）。

const zhipuMonitorPath = "/api/monitor/usage/quota/limit"

// ZhipuUsageWindow 归一化后的单窗口用量（5小时 / 每周）
type ZhipuUsageWindow struct {
	UsedPercent float64 `json:"used_percent"` // 已用百分比
	ResetAt     int64   `json:"reset_at"`     // 重置时间（unix 秒），0 表示未知
}

// ZhipuMcpMonthly MCP 每月调用次数限制（TIME_LIMIT）
type ZhipuMcpMonthly struct {
	Used  float64 `json:"used"`
	Total float64 `json:"total"`
}

// ZhipuUsageInfo 渠道 usage_info 字段的存储结构
type ZhipuUsageInfo struct {
	Provider   string            `json:"provider"` // "zhipu"
	Level      string            `json:"level,omitempty"`
	FiveHour   *ZhipuUsageWindow `json:"five_hour,omitempty"`
	Weekly     *ZhipuUsageWindow `json:"weekly,omitempty"`
	McpMonthly *ZhipuMcpMonthly  `json:"mcp_monthly,omitempty"`
	UpdatedAt  int64             `json:"updated_at"`
}

type zhipuQuotaLimitEntry struct {
	Type          string  `json:"type"`
	Percentage    float64 `json:"percentage"`
	NextResetTime any     `json:"nextResetTime"`
	Usage         float64 `json:"usage"`
	CurrentValue  float64 `json:"currentValue"`
	Remaining     float64 `json:"remaining"`
}

type zhipuQuotaLimitResponse struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Success *bool  `json:"success"`
	Data    struct {
		Level  string                 `json:"level"`
		Limits []zhipuQuotaLimitEntry `json:"limits"`
	} `json:"data"`
}

// IsZhipuUsageChannel 判断渠道是否可查询智谱官方套餐用量：
// 智谱渠道（16/26），或 base_url 指向智谱域名的 Claude 渠道（如 GLM Coding Plan 常见配置）。
func IsZhipuUsageChannel(channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	switch channel.Type {
	case constant.ChannelTypeZhipu, constant.ChannelTypeZhipu_v4:
		return true
	case constant.ChannelTypeAnthropic:
		return isZhipuHost(zhipuChannelDomain(channel.GetBaseURL()))
	}
	return false
}

// ResolveZhipuMonitorURL 根据渠道 base_url 解析用量查询端点。
// 支持 glm-coding-plan(-international) 预设别名，其余取 scheme://host，默认国内域名。
func ResolveZhipuMonitorURL(baseURL string) string {
	return zhipuChannelDomain(baseURL) + zhipuMonitorPath
}

func zhipuChannelDomain(baseURL string) string {
	base := strings.TrimSpace(baseURL)
	if special, ok := constant.ChannelSpecialBases[base]; ok && special.ClaudeBaseURL != "" {
		base = special.ClaudeBaseURL
	}
	if base == "" {
		base = constant.GetChannelBaseURL(constant.ChannelTypeZhipu_v4)
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return constant.GetChannelBaseURL(constant.ChannelTypeZhipu_v4)
}

func isZhipuHost(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{"bigmodel.cn", "bigmodel.com", "z.ai"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func FetchZhipuUsage(
	ctx context.Context,
	client *http.Client,
	monitorURL string,
	apiKey string,
) (statusCode int, body []byte, err error) {
	if client == nil {
		return 0, nil, fmt.Errorf("nil http client")
	}
	monitorURL = strings.TrimSpace(monitorURL)
	if monitorURL == "" {
		return 0, nil, fmt.Errorf("empty monitor url")
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return 0, nil, fmt.Errorf("empty api key")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, monitorURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// ParseZhipuUsage 将智谱 quota/limit 响应归一化：
// TOKENS_LIMIT 按重置时间升序，前两个分别对应 5小时 / 每周窗口；TIME_LIMIT 为 MCP 每月次数。
func ParseZhipuUsage(body []byte) (*ZhipuUsageInfo, error) {
	var resp zhipuQuotaLimitResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析智谱用量响应失败: %w", err)
	}
	if len(resp.Data.Limits) == 0 {
		msg := strings.TrimSpace(resp.Msg)
		if msg == "" {
			msg = "响应中无用量数据"
		}
		return nil, fmt.Errorf("智谱用量查询失败: %s", msg)
	}

	info := &ZhipuUsageInfo{
		Provider:  "zhipu",
		Level:     resp.Data.Level,
		UpdatedAt: time.Now().Unix(),
	}

	var tokenWindows []ZhipuUsageWindow
	for _, limit := range resp.Data.Limits {
		switch limit.Type {
		case "TOKENS_LIMIT":
			tokenWindows = append(tokenWindows, ZhipuUsageWindow{
				UsedPercent: limit.Percentage,
				ResetAt:     parseZhipuResetTime(limit.NextResetTime),
			})
		case "TIME_LIMIT":
			info.McpMonthly = &ZhipuMcpMonthly{
				Used:  limit.CurrentValue,
				Total: limit.Usage,
			}
		}
	}

	sort.SliceStable(tokenWindows, func(i, j int) bool {
		if tokenWindows[i].ResetAt == tokenWindows[j].ResetAt {
			return tokenWindows[i].UsedPercent < tokenWindows[j].UsedPercent
		}
		if tokenWindows[i].ResetAt == 0 {
			return false
		}
		if tokenWindows[j].ResetAt == 0 {
			return true
		}
		return tokenWindows[i].ResetAt < tokenWindows[j].ResetAt
	})
	if len(tokenWindows) > 0 {
		fiveHour := tokenWindows[0]
		info.FiveHour = &fiveHour
	}
	if len(tokenWindows) > 1 {
		weekly := tokenWindows[1]
		info.Weekly = &weekly
	}
	return info, nil
}

// parseZhipuResetTime 官方文档未明确 nextResetTime 格式，兼容 unix 秒/毫秒与常见字符串写法，解析失败返回 0。
func parseZhipuResetTime(v any) int64 {
	switch val := v.(type) {
	case float64:
		return zhipuUnixFromFloat(val)
	case json.Number:
		if f, err := val.Float64(); err == nil {
			return zhipuUnixFromFloat(f)
		}
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return 0
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return zhipuUnixFromFloat(f)
		}
		layouts := []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04:05 -0700 CST"}
		for _, layout := range layouts {
			if t, err := time.ParseInLocation(layout, s, time.FixedZone("CST", 8*3600)); err == nil {
				return t.Unix()
			}
		}
	}
	return 0
}

func zhipuUnixFromFloat(f float64) int64 {
	switch {
	case f > 1e12: // 毫秒时间戳
		return int64(f / 1000)
	case f > 1e9: // 秒时间戳
		return int64(f)
	default:
		return 0
	}
}
