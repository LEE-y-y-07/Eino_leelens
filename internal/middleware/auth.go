package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Auth 返回一个共享令牌鉴权中间件。
//
// token 为空时直接放行（关闭鉴权，适配自托管/本地开发，保持原有行为不变）。
// 令牌来源：HTTP 头 `Authorization: Bearer <token>`，或 query 参数 `?token=`
// （供 WebSocket 握手与 MCP 客户端使用，它们不便设置自定义头）。
//
// 豁免（无需令牌即可访问）：CORS 预检(OPTIONS)、OpenAPI 发现端点、
// 公开会话浏览/只读视图、文档跳转。
func Auth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}
		if c.Request.Method == http.MethodOptions || isExempt(c.Request.URL.Path) {
			c.Next()
			return
		}
		if extractToken(c) != token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// extractToken 依次从 Authorization 头与 query 参数提取令牌。
func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
		return strings.TrimSpace(h)
	}
	return c.Query("token")
}

// isExempt 判断路径是否属于无需鉴权的公开/发现类端点。
func isExempt(path string) bool {
	switch {
	case path == "/.well-known/openapi.yaml":
		return true
	// 文档跳转：/api/doc/:id/redirect
	case strings.HasPrefix(path, "/api/doc/") && strings.HasSuffix(path, "/redirect"):
		return true
	// 公开会话列表：/api/repositories/:id/chat/sessions/public
	case strings.HasSuffix(path, "/chat/sessions/public"):
		return true
	// 公开会话只读视图：/api/repositories/:id/chat/sessions/:sid/view
	case strings.Contains(path, "/chat/sessions/") && strings.HasSuffix(path, "/view"):
		return true
	}
	return false
}
