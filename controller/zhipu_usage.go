package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetZhipuChannelUsage 查询智谱 GLM Coding Plan 官方套餐用量并持久化到渠道 usage_info。
func GetZhipuChannelUsage(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ch, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if ch == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel not found"})
		return
	}

	usage, err := fetchZhipuUsageForChannel(c, ch)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": usage})
}

// fetchZhipuUsageForChannel 查询智谱官方套餐用量并持久化，返回可序列化的用量信息。
func fetchZhipuUsageForChannel(c *gin.Context, ch *model.Channel) (*service.ZhipuUsageInfo, error) {
	if !service.IsZhipuUsageChannel(ch) {
		return nil, fmt.Errorf("仅支持智谱渠道或指向智谱域名的 Claude 渠道")
	}
	if ch.ChannelInfo.IsMultiKey {
		return nil, fmt.Errorf("多密钥渠道不支持用量查询")
	}

	client, err := service.GetHttpClientWithProxy(ch.GetSetting().Proxy)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	monitorURL := service.ResolveZhipuMonitorURL(ch.GetBaseURL())
	statusCode, body, err := service.FetchZhipuUsage(ctx, client, monitorURL, strings.TrimSpace(ch.Key))
	if err != nil {
		common.SysError("failed to fetch zhipu usage: " + err.Error())
		return nil, fmt.Errorf("获取智谱套餐用量失败，请稍后重试")
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("智谱接口返回错误: %d %s", statusCode, zhipuUpstreamErrorMessage(body))
	}

	usage, err := service.ParseZhipuUsage(body)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to parse zhipu usage (channel %d): %v", ch.Id, err))
		return nil, err
	}

	if data, err := common.Marshal(usage); err == nil {
		ch.UpdateUsageInfo(string(data))
	} else {
		common.SysError(fmt.Sprintf("failed to marshal zhipu usage (channel %d): %v", ch.Id, err))
	}
	return usage, nil
}

// refreshZhipuChannelUsage 批量/定时任务用：静默刷新智谱渠道套餐用量，失败仅记日志。
func refreshZhipuChannelUsage(ch *model.Channel) {
	client, err := service.GetHttpClientWithProxy(ch.GetSetting().Proxy)
	if err != nil {
		common.SysError("failed to get http client for zhipu usage: " + err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	monitorURL := service.ResolveZhipuMonitorURL(ch.GetBaseURL())
	statusCode, body, err := service.FetchZhipuUsage(ctx, client, monitorURL, strings.TrimSpace(ch.Key))
	if err != nil {
		common.SysError(fmt.Sprintf("failed to fetch zhipu usage (channel %d): %v", ch.Id, err))
		return
	}
	if statusCode != http.StatusOK {
		common.SysError(fmt.Sprintf("zhipu usage upstream status (channel %d): %d", ch.Id, statusCode))
		return
	}
	usage, err := service.ParseZhipuUsage(body)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to parse zhipu usage (channel %d): %v", ch.Id, err))
		return
	}
	if data, err := common.Marshal(usage); err == nil {
		ch.UpdateUsageInfo(string(data))
	}
}

func zhipuUpstreamErrorMessage(body []byte) string {
	var errResp struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Msg != "" {
		return errResp.Msg
	}
	return ""
}
