package adkagents

import (
	"net"
	"net/http"
	"time"
)

// ClaudeHeaderTransport 伪装为 Claude CLI 的 HTTP Transport
type ClaudeHeaderTransport struct {
	rt http.RoundTripper
}

// newRobustTransport 构造带合理超时的 HTTP Transport ——
// 关键：不设置 http.Client.Timeout（流式响应可以合法持续数分钟），
// 但必须设置以下分项超时，避免单次 LLM 调用因为 TCP/TLS/响应头卡死而无限等待：
//   - DialContext.Timeout：建立 TCP 连接的最大时间
//   - TLSHandshakeTimeout：TLS 握手超时
//   - ResponseHeaderTimeout：从发出请求到收到首个响应头的最大等待 —— 中继半连接最常见的卡点
//   - IdleConnTimeout：空闲连接复用的有效期
//   - ExpectContinueTimeout：100-Continue 等待
func newRobustTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// NewClaudeHeaderTransport 创建一个伪装为 Claude CLI 的 Transport
func NewClaudeHeaderTransport() *ClaudeHeaderTransport {
	return &ClaudeHeaderTransport{
		rt: newRobustTransport(),
	}
}

// RoundTrip 实现 http.RoundTripper 接口，注入 Claude 相关的 headers
func (t *ClaudeHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Claude CLI 特有的 headers
	headers := map[string]string{
		"User-Agent":                                "claude-cli/2.1.81 (external, sdk-cli)",
		"X-App":                                     "cli",
		"X-Stainless-Lang":                          "js",
		"X-Stainless-Os":                            "MacOS",
		"X-Stainless-Arch":                          "arm64",
		"X-Stainless-Runtime":                       "node",
		"X-Stainless-Runtime-Version":               "v24.3.0",
		"X-Stainless-Package-Version":               "0.74.0",
		"X-Stainless-Retry-Count":                   "0",
		"X-Stainless-Timeout":                       "600",
		"Anthropic-Version":                         "2023-06-01",
		"Anthropic-Dangerous-Direct-Browser-Access": "true",
		"Connection":                                "keep-alive",
		"Anthropic-Beta":                            "claude-code-20250219,interleaved-thinking-2025-05-14,prompt-caching-scope-2026-01-05,effort-2025-11-24",
	}

	for k, v := range headers {
		req.Header.Set(k, v) // 强制覆盖，确保 Claude Code 伪装生效
	}

	return t.rt.RoundTrip(req)
}

// NewClaudeHTTPClient 创建伪装为 Claude CLI 的 HTTP Client
func NewClaudeHTTPClient() *http.Client {
	return &http.Client{
		Transport: NewClaudeHeaderTransport(),
	}
}
