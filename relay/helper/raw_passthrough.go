package helper

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// RawPassthroughWrittenKey 是 context flag 的 key。
// 透传分支在直接写入上游响应后设置它为 true，最外层 defer 据此跳过重复写。
const RawPassthroughWrittenKey = "raw_passthrough_written"

// RawPassthroughUpstreamError 把上游的非 200 响应原样转发给客户端。
//
// 用于开启了 GroupPassThrough 的分组：不经过 RelayErrorHandler 重新包装，
// 直接把上游的 status code + body + Content-Type 转发给客户端。
// 这样客户端能收到上游原汁原味的错误（例如 GLM 的 1211 模型不存在），
// 而不是被 new-api 统一包装成 OpenAI 格式。
//
// 调用前提：必须在 adaptor.DoRequest 返回后、DoResponse 之前调用，
// 此时 gin Writer 还没写过任何东西，可以自由设置状态码和写入 body。
//
// 返回一个 SkipRetry 的 NewAPIError，并通过 c.Set(RawPassthroughWrittenKey, true)
// 标记"响应已写"，上层 defer 应据此跳过 c.JSON 重复写入。
func RawPassthroughUpstreamError(c *gin.Context, httpResp *http.Response) *types.NewAPIError {
	// 1. 读取上游原始响应体（HTTP body 只能读一次，读完转发）
	bodyBytes, _ := io.ReadAll(httpResp.Body)
	_ = httpResp.Body.Close()

	// 2. 保留上游的 Content-Type（错误响应多为 application/json，也可能是别的）
	contentType := httpResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	// 3. 原样写状态码 + body 到客户端（c.Data 会设置 Content-Type 和状态码）
	c.Data(httpResp.StatusCode, contentType, bodyBytes)

	// 4. 标记"已写响应"，并返回 SkipRetry 错误冒泡到上层
	c.Set(RawPassthroughWrittenKey, true)
	return types.NewErrorWithStatusCode(
		fmt.Errorf("raw passthrough: upstream status %d", httpResp.StatusCode),
		types.ErrorCodeBadResponseStatusCode,
		httpResp.StatusCode,
		types.ErrOptionWithSkipRetry(),
	)
}
