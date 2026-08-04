package helper

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRawPassthroughUpstreamError 验证开启透传的分组在各种上游 API 错误下，
// 都能把上游的原始状态码、响应体、Content-Type 原样转发给客户端，
// 并正确标记 written flag 让上层 defer 跳过重复写。
//
// 覆盖的状态码维度：400(模型不存在)、401(鉴权)、403(禁止)、408(超时)、
// 413(请求过大)、429(限流)、500/502/503/504(服务端错误)。
// 这些是透传分组实际会遇到的上游错误，逐一确认它们都能被透传而非被重新包装。
func TestRawPassthroughUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 上游错误响应的几种典型形态（状态码 -> body + Content-Type）
	cases := []struct {
		name        string
		statusCode  int
		body        string
		contentType string
	}{
		{"400 模型不存在(GLM 1211)", 400, `{"error":{"code":"1211","message":"模型不存在，请检查模型代码。"}}`, "application/json"},
		{"401 鉴权失败", 401, `{"error":{"message":"invalid api key"}}`, "application/json"},
		{"403 禁止访问", 403, `{"error":{"message":"forbidden"}}`, "application/json"},
		{"408 请求超时", 408, `{"error":{"message":"request timeout"}}`, "application/json"},
		{"413 请求体过大", 413, `{"error":{"message":"request entity too large"}}`, "application/json"},
		{"429 限流(5小时限制等)", 429, `{"error":{"code":"421","message":"请求过于频繁，请稍后重试"}}`, "application/json"},
		{"500 服务端错误", 500, `{"error":{"message":"internal server error"}}`, "application/json"},
		{"502 网关错误", 502, `Bad Gateway`, "text/plain"},
		{"503 服务不可用", 503, `{"error":"service unavailable"}`, "application/json"},
		{"504 网关超时", 504, `Gateway Timeout`, "text/plain"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 构造一个 fake 上游响应
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			upstreamResp := &http.Response{
				StatusCode: tc.statusCode,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			upstreamResp.Header.Set("Content-Type", tc.contentType)

			// 调用被测函数
			err := RawPassthroughUpstreamError(c, upstreamResp)

			// 1. 返回了非 nil 的 NewAPIError，且带 SkipRetry 标记
			require.NotNil(t, err, "应返回非 nil 错误冒泡到上层")
			assert.True(t, types.IsSkipRetryError(err), "透传错误必须是 SkipRetry，避免重试")

			// 2. written flag 已设置（上层 defer 据此跳过重复写）
			assert.True(t, c.GetBool(RawPassthroughWrittenKey),
				"必须设置 raw_passthrough_written=true，否则 defer 会重复写响应")

			// 3. 客户端收到的状态码 = 上游原始状态码（原样转发，未被重映射）
			assert.Equal(t, tc.statusCode, recorder.Code,
				"客户端应收到上游原始状态码 %d", tc.statusCode)

			// 4. 客户端收到的 body = 上游原始 body（一字不改）
			assert.Equal(t, tc.body, recorder.Body.String(),
				"客户端应收到上游原始响应体，不经过 RelayErrorHandler 包装")

			// 5. Content-Type 保留上游的（错误响应也可能是 text/plain）
			assert.Equal(t, tc.contentType, recorder.Header().Get("Content-Type"),
				"应保留上游原始 Content-Type")
		})
	}
}

// TestRawPassthroughDefaultContentType 验证上游响应没带 Content-Type 时，
// 兜底为 application/json（避免客户端无法解析）。
func TestRawPassthroughDefaultContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			upstreamResp := &http.Response{
				StatusCode: 400,
				Header:     http.Header{}, // 故意不设 Content-Type
				Body:       io.NopCloser(strings.NewReader(`{"error":"x"}`)),
			}

	_ = RawPassthroughUpstreamError(c, upstreamResp)

	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"),
		"上游未提供 Content-Type 时应兜底为 application/json")
	assert.Equal(t, 400, recorder.Code)
}
