package helper

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// WriteClaudeStreamError 写入一条 Anthropic 格式的 SSE 错误 event，
// 用于 Claude 流式请求在中间件阶段（模型未配置、鉴权失败、限流等）出错时友好返回。
//
// 为什么需要这个：Claude Code / VS Code 扩展用 stream:true 发请求，期望收到
// text/event-stream。如果中间件错误用 application/json 返回，流式客户端的 SSE
// 解析器无法处理，直接显示成"无响应"。所以流式错误必须用 SSE event: error 返回。
//
// 参考现有的 WriteInsufficientQuotaClaudeStreamReply，复用同样的 c.Render + CustomEvent 写法。
//
// 参数：
//   - statusCode: HTTP 状态码（404/400/401 等，按错误类型，遵循 Anthropic 规范）
//   - errorType: Anthropic 错误类型（not_found_error / invalid_request_error / api_error 等）
//   - message: 错误消息
func WriteClaudeStreamError(c *gin.Context, statusCode int, errorType, message string) {
	SetEventStreamHeaders(c)
	c.Status(statusCode)

	payload := map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    errorType,
			"message": message,
		},
	}
	jsonData, _ := common.Marshal(payload)

	// Anthropic SSE 每条消息需要 event: 前缀（规范要求，Claude Code 严格依赖此格式）
	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: error\n")})
	c.Render(-1, common.CustomEvent{Data: "data: " + string(jsonData)})
	_ = FlushWriter(c)
}
