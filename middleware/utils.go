package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	codeStr := ""
	if len(code) > 0 {
		codeStr = string(code[0])
	}
	userId := c.GetInt("id")
	messageWithId := common.MessageWithRequestId(message, c.GetString(common.RequestIdKey))

	// 插件路由准备好的请求优先按任务插件协议响应错误（上游逻辑）。
	_, preparedPluginRoute := c.Get(pluginruntime.ContextKeyRouteRequest)
	pluginResponded := preparedPluginRoute && RespondTaskPluginError(c, &dto.TaskError{
		Code:       codeStr,
		Message:    message,
		StatusCode: statusCode,
	})

	if !pluginResponded {
		// 按请求格式选择错误结构：Claude 请求（/v1/messages）用 Anthropic 标准格式，
		// 其他保持 OpenAI 格式。这样 Claude Code / VS Code 扩展能正确识别内部错误
		// （模型未配置、鉴权失败、限流等），不会因格式不匹配而显示成"无响应"。
		if isClaudeRequest(c) {
			errorType := inferClaudeErrorType(codeStr)
			// Anthropic 规范状态码：模型不存在=404，其他按原状态码
			claudeStatus := mapClaudeStatusCode(statusCode, codeStr)

			if isStreamRequest(c) {
				// 流式：Claude 客户端期望 SSE，用 event: error 返回，否则流式解析器无法处理
				helper.WriteClaudeStreamError(c, claudeStatus, errorType, messageWithId)
			} else {
				// 非流式：JSON 格式
				c.JSON(claudeStatus, gin.H{
					"type": "error",
					"error": gin.H{
						"type":    errorType,
						"message": messageWithId,
					},
				})
			}
		} else {
			c.JSON(statusCode, gin.H{
				"error": gin.H{
					"message": messageWithId,
					"type":    "new_api_error",
					"code":    codeStr,
				},
			})
		}
	}
	c.Abort()
	logger.LogError(c.Request.Context(), fmt.Sprintf("user %d | %s", userId, message))
}

// isClaudeRequest 判断当前请求是否为 Claude/Anthropic 格式（/v1/messages）。
// 中间件阶段拿不到 relayFormat（它是 Relay 函数参数，不在 gin.Context），
// 通过路径前缀判断，参考 middleware/performance.go 的同样做法。
func isClaudeRequest(c *gin.Context) bool {
	return strings.HasPrefix(c.Request.URL.Path, "/v1/messages")
}

// isStreamRequest 判断请求是否为流式（探测 body 里的 stream:true）。
// 用 common.GetBodyStorage 读 body（内部有 BodyStorage 缓存，可重放读取，
// 与 getModelRequest 的 UnmarshalBodyReusable 共享同一份缓存）。
// 探测失败（读不到 body）时保守返回 false，走非流式 JSON 路径。
func isStreamRequest(c *gin.Context) bool {
	storage, err := common.GetBodyStorage(c)
	if err != nil || storage == nil {
		return false
	}
	// 读出来探测
	body, err := io.ReadAll(storage)
	if err != nil {
		return false
	}
	// 读完后重置到开头，确保后续中间件（getModelRequest）能正常复用 body
	_, _ = storage.Seek(0, io.SeekStart)
	return strings.Contains(string(body), `"stream":true`) || strings.Contains(string(body), `"stream": true`)
}

// mapClaudeStatusCode 把 new-api 内部状态码映射成 Anthropic 规范状态码。
// 模型不存在 Anthropic 规范是 404，而 new-api 内部用 503；其余按原状态码。
func mapClaudeStatusCode(statusCode int, code string) int {
	switch code {
	case string(types.ErrorCodeModelNotFound): // model_not_found → 404
		return http.StatusNotFound
	default:
		return statusCode
	}
}

// inferClaudeErrorType 把 new-api 内部错误码映射成 Anthropic 规范的错误类型。
// Anthropic 规范的 error.type 是固定枚举：invalid_request_error / authentication_error /
// permission_error / not_found_error / rate_limit_error / api_error / overloaded_error 等。
// 未识别的 code（含空值）统一归为 api_error。
func inferClaudeErrorType(code string) string {
	switch code {
	case string(types.ErrorCodeModelNotFound): // model_not_found
		return "not_found_error"
	case string(types.ErrorCodeInvalidRequest): // invalid_request
		return "invalid_request_error"
	case string(types.ErrorCodeAccessDenied): // access_denied
		return "permission_error"
	default:
		// 空 code 或其他未识别错误码：通用 API 错误
		return "api_error"
	}
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	c.JSON(statusCode, gin.H{
		"description": description,
		"type":        "new_api_error",
		"code":        code,
	})
	c.Abort()
	logger.LogError(c.Request.Context(), description)
}
