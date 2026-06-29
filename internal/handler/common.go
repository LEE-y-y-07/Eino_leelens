package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// parseUintParam 解析路径参数为 uint；失败时写入 400 响应并返回 ok=false。
// 统一各 handler 里重复的 strconv.ParseUint(c.Param(...), 10, 32) 模式，
// 新增 handler 优先使用本函数。
func parseUintParam(c *gin.Context, name string) (uint, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的" + name})
		return 0, false
	}
	return uint(v), true
}
