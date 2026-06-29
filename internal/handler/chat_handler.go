package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gitee.com/li-yuyanglee/leelens-backend/config"
	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gitee.com/li-yuyanglee/leelens-backend/internal/pkg/adkagents"
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
	"gitee.com/li-yuyanglee/leelens-backend/internal/service"
	"k8s.io/klog/v2"
)

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域，生产环境应该配置具体域名
	},
}

// ChatHandler 对话处理器
type ChatHandler struct {
	chatService  service.ChatService
	repoService  *service.RepositoryService
	docService   *service.DocumentService
	hub          *ChatHub
	agentFactory *adkagents.AgentFactory
	apiKeyRepo   repository.APIKeyRepository
	summarizer   *service.Summarizer
	ragService   *service.RAGService
}

// NewChatHandler 创建处理器
func NewChatHandler(chatService service.ChatService, repoService *service.RepositoryService, docService *service.DocumentService, agentFactory *adkagents.AgentFactory, apiKeyRepo repository.APIKeyRepository, ragService *service.RAGService) *ChatHandler {
	return &ChatHandler{
		chatService:  chatService,
		repoService:  repoService,
		docService:   docService,
		hub:          NewChatHub(),
		agentFactory: agentFactory,
		apiKeyRepo:   apiKeyRepo,
		summarizer:   service.NewSummarizer(agentFactory),
		ragService:   ragService,
	}
}

// GetHub 获取Hub（用于启动）
func (h *ChatHandler) GetHub() *ChatHub {
	return h.hub
}

// RegisterRoutes 注册路由
func (h *ChatHandler) RegisterRoutes(r *gin.RouterGroup) {
	chat := r.Group("/repositories/:id/chat")
	{
		// 会话管理
		chat.POST("/sessions", h.CreateSession)
		chat.GET("/sessions", h.ListSessions)
		chat.GET("/sessions/public", h.ListPublicSessions)
		chat.GET("/sessions/:session_id", h.GetSession)
		chat.GET("/sessions/:session_id/view", h.GetSessionView)
		chat.DELETE("/sessions/:session_id", h.DeleteSession)
		chat.PUT("/sessions/:session_id/visibility", h.UpdateSessionVisibility)

		// WebSocket
		chat.GET("/sessions/:session_id/stream", h.WebSocket)
	}
}

// CreateSession 创建会话
func (h *ChatHandler) CreateSession(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的仓库ID"})
		return
	}

	// 可选 body：{"doc_id": 123} 表示这是"就此文档提问"会话；不传或 doc_id=0 视为全局对话
	var body struct {
		DocID uint `json:"doc_id"`
	}
	_ = c.ShouldBindJSON(&body) // 容忍空/非 JSON body —— 老前端不传也兼容

	session, err := h.chatService.CreateSession(c.Request.Context(), uint(repoID), body.DocID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, session)
}

// ListSessions 获取会话列表（可选 ?doc_id= 仅返回该文档下会话；不传或 0 返回全部）
func (h *ChatHandler) ListSessions(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的仓库ID"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	docID, _ := strconv.ParseUint(c.DefaultQuery("doc_id", "0"), 10, 32)

	sessions, total, err := h.chatService.ListSessions(c.Request.Context(), uint(repoID), uint(docID), page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items":     sessions,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetSession 获取会话详情
func (h *ChatHandler) GetSession(c *gin.Context) {
	sessionID := c.Param("session_id")

	session, err := h.chatService.GetSession(c.Request.Context(), sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	// 获取消息列表（默认10条）
	messages, err := h.chatService.ListMessages(c.Request.Context(), sessionID, 10, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session":  session,
		"messages": messages,
	})
}

// DeleteSession 删除会话
func (h *ChatHandler) DeleteSession(c *gin.Context) {
	sessionID := c.Param("session_id")

	if err := h.chatService.DeleteSession(c.Request.Context(), sessionID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}

// ListPublicSessions 获取公开会话列表
func (h *ChatHandler) ListPublicSessions(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的仓库ID"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	sessions, total, err := h.chatService.ListPublicSessions(c.Request.Context(), uint(repoID), page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items":     sessions,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetSessionView 获取会话详情（供展示使用）
func (h *ChatHandler) GetSessionView(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的仓库ID"})
		return
	}

	sessionID := c.Param("session_id")

	// 获取会话
	session, err := h.chatService.GetSession(c.Request.Context(), sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	// 验证会话属于该仓库
	if session.RepoID != uint(repoID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "会话不属于该仓库"})
		return
	}

	// 权限检查：私有会话需要验证
	if session.Visibility == "private" {
		// TODO: 获取当前用户ID并验证是否为创建者
		// 暂时允许访问，后续添加用户认证后再完善
	}

	// 获取消息列表
	messages, err := h.chatService.ListMessages(c.Request.Context(), sessionID, 100, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session":  session,
		"messages": messages,
	})
}

// UpdateSessionVisibility 更新会话可见性
func (h *ChatHandler) UpdateSessionVisibility(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的仓库ID"})
		return
	}

	sessionID := c.Param("session_id")

	var req struct {
		Visibility string `json:"visibility" binding:"required,oneof=public private"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数"})
		return
	}

	// 获取会话
	session, err := h.chatService.GetSession(c.Request.Context(), sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	// 验证会话属于该仓库
	if session.RepoID != uint(repoID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "会话不属于该仓库"})
		return
	}

	// 注：当前为共享令牌鉴权模型，无独立用户身份，已通过鉴权即视为有权操作。
	// 若将来引入用户账户（启用 ChatSession.CreatedBy），应在此校验创建者身份。

	// 更新可见性
	if err := h.chatService.UpdateSessionVisibility(c.Request.Context(), sessionID, req.Visibility); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session_id": sessionID,
		"visibility": req.Visibility,
	})
}

// WebSocket WebSocket连接
func (h *ChatHandler) WebSocket(c *gin.Context) {
	repoID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的仓库ID"})
		return
	}

	sessionID := c.Param("session_id")

	// 验证会话存在且属于该仓库
	session, err := h.chatService.GetSession(c.Request.Context(), sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}

	if session.RepoID != uint(repoID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "会话不属于该仓库"})
		return
	}

	// 升级WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "WebSocket升级失败"})
		return
	}

	// 创建客户端
	client := &Client{
		hub:       h.hub,
		conn:      conn,
		send:      make(chan []byte, 256),
		sessionID: sessionID,
		repoID:    uint(repoID),
		stopChan:  make(chan struct{}),
	}

	// 注册到Hub
	h.hub.register <- client

	// 启动读写协程
	go client.writePump()
	go client.readPump(h)
}

// Client 表示一个WebSocket客户端
type Client struct {
	hub       *ChatHub
	conn      *websocket.Conn
	send      chan []byte
	sessionID string
	repoID    uint
	stopChan  chan struct{}
	mu        sync.Mutex
	closed    bool // 标记连接是否已关闭
}

// readPump 读取消息
func (c *Client) readPump(h *ChatHandler) {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				fmt.Printf("WebSocket error: %v\n", err)
			}
			break
		}

		// 解析消息
		var msg ClientMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			c.sendError("INVALID_MESSAGE", "消息格式错误")
			continue
		}

		// 处理消息
		switch msg.Type {
		case "message":
			h.handleMessage(c, &msg)
		case "stop":
			h.handleStop(c)
		case "ping":
			c.sendPong()
		default:
			c.sendError("UNKNOWN_TYPE", "未知的消息类型")
		}
	}
}

// writePump 写入消息
func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			c.conn.WriteMessage(websocket.TextMessage, message)

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// sendError 发送错误消息（线程安全）
func (c *Client) sendError(code, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	event := ServerMessage{
		Type:      "error",
		ID:        generateEventID(),
		Timestamp: time.Now().UnixMilli(),
		Payload: ErrorPayload{
			Code:      code,
			Message:   message,
			Retryable: false,
		},
	}
	data, _ := json.Marshal(event)
	select {
	case c.send <- data:
	default:
	}
}

// sendPong 发送pong（线程安全）
func (c *Client) sendPong() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	event := ServerMessage{
		Type:      "pong",
		ID:        generateEventID(),
		Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(event)
	select {
	case c.send <- data:
	default:
	}
}

// handleMessage 处理用户消息
func (h *ChatHandler) handleMessage(client *Client, msg *ClientMessage) {
	ctx := context.Background()

	// 检查消息数量限制
	count, err := h.chatService.CountMessages(ctx, client.sessionID)
	if err != nil {
		client.sendError("INTERNAL_ERROR", "检查消息数量失败")
		return
	}

	if count >= 1000 {
		client.sendError("MESSAGE_LIMIT", "会话消息数量已达上限")
		return
	}

	// 保存用户消息
	userMsg, err := h.chatService.CreateUserMessage(ctx, client.sessionID, msg.Content)
	if err != nil {
		client.sendError("INTERNAL_ERROR", "保存消息失败")
		return
	}

	// 如果是第一条消息，先用截断标题占位，再异步用 LLM 生成更贴切的标题
	if count == 0 {
		title := msg.Content
		if len(title) > 50 {
			title = title[:50] + "..."
		}
		h.chatService.UpdateSessionTitle(ctx, client.sessionID, title)

		if h.summarizer != nil {
			sessionID := client.sessionID
			firstMsg := msg.Content
			go func() {
				bg := context.Background()
				t, err := h.summarizer.Summarize(bg, "请为下面这条用户的首条提问生成一个不超过 20 字的简洁会话标题，只输出标题本身，不要任何解释或标点包裹：", firstMsg)
				if err != nil || t == "" {
					return // 保留截断版标题
				}
				t = strings.Trim(t, "\"'`　 \n")
				if r := []rune(t); len(r) > 30 {
					t = string(r[:30])
				}
				if t != "" {
					h.chatService.UpdateSessionTitle(bg, sessionID, t)
				}
			}()
		}
	}

	// 启动Agent执行
	go h.runAgent(client, userMsg)
}

// handleStop 处理停止请求
func (h *ChatHandler) handleStop(client *Client) {
	client.mu.Lock()
	defer client.mu.Unlock()

	close(client.stopChan)
	client.stopChan = make(chan struct{})
}

// historyTokenBudget 返回历史装配的 token 预算（带默认）。
func (h *ChatHandler) historyTokenBudget() int {
	if b := config.GetConfig().Chat.HistoryTokenBudget; b > 0 {
		return b
	}
	return 6000
}

// estimateTokens 粗略估算文本 token 数（中英混合按约 2 字符/token）。
func estimateTokens(s string) int {
	return len([]rune(s))/2 + 1
}

// chatRoleLabel 把存储角色映射成摘要文本里的中文标签。
func chatRoleLabel(role string) string {
	if role == "assistant" {
		return "助手"
	}
	return "用户"
}

// buildHistoryMessages 把会话历史按 token 预算装配为 ADK 消息（时间正序）。
// 取较大窗口后从最新往旧累加：预算内的保留为原始消息；超出预算的更早消息压成
// 一段「早前对话摘要」系统消息置于最前。assistantMsgID 为本轮占位消息需跳过。
func (h *ChatHandler) buildHistoryMessages(ctx context.Context, sessionID, assistantMsgID string) []*schema.Message {
	raw, err := h.chatService.ListMessages(ctx, sessionID, 200, nil) // DESC：新→旧
	if err != nil {
		klog.Warningf("[ChatHandler] 加载历史消息失败: %v", err)
		return nil
	}
	budget := h.historyTokenBudget()
	var kept []*model.ChatMessage     // DESC，预算内保留
	var overflow []*model.ChatMessage // DESC，超预算的更早消息
	used := 0
	for _, m := range raw {
		if m.MessageID == assistantMsgID {
			continue
		}
		if m.Status != "completed" && m.Status != "streaming" {
			continue
		}
		t := estimateTokens(m.Content)
		if len(kept) == 0 || used+t <= budget {
			kept = append(kept, m)
			used += t
		} else {
			overflow = append(overflow, m)
		}
	}

	var out []*schema.Message
	// 摘要更早的 overflow（DESC → 反转成正序文本再摘要）
	if len(overflow) > 0 && h.summarizer != nil {
		var sb strings.Builder
		for i := len(overflow) - 1; i >= 0; i-- {
			sb.WriteString(chatRoleLabel(overflow[i].Role))
			sb.WriteString("：")
			sb.WriteString(overflow[i].Content)
			sb.WriteString("\n")
		}
		if sum, sErr := h.summarizer.Summarize(ctx, "请将以下早前的对话压缩为简洁摘要，保留关键事实、结论与未决问题：", sb.String()); sErr == nil && sum != "" {
			out = append(out, &schema.Message{Role: schema.System, Content: "## 早前对话摘要\n" + sum})
		} else if sErr != nil {
			klog.Warningf("[ChatHandler] 历史摘要失败，跳过摘要段: %v", sErr)
		}
	}
	// kept（DESC）反转为时间正序追加
	for i := len(kept) - 1; i >= 0; i-- {
		role := schema.User
		if kept[i].Role == "assistant" {
			role = schema.Assistant
		}
		out = append(out, &schema.Message{Role: role, Content: kept[i].Content})
	}
	return out
}

// maybeUpdateSessionSummary 异步刷新会话摘要（L2 记忆）。每累计若干条消息才重算，控制成本。
func (h *ChatHandler) maybeUpdateSessionSummary(sessionID string) {
	if h.summarizer == nil {
		return
	}
	ctx := context.Background()
	count, err := h.chatService.CountMessages(ctx, sessionID)
	if err != nil || count < 4 || count%4 != 0 {
		return
	}
	msgs, err := h.chatService.ListMessages(ctx, sessionID, 40, nil) // DESC：新→旧
	if err != nil || len(msgs) == 0 {
		return
	}
	var sb strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Status != "completed" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		sb.WriteString(chatRoleLabel(m.Role))
		sb.WriteString("：")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	sum, err := h.summarizer.Summarize(ctx, "请用 2-4 句话概括下面这段关于某代码仓库的对话，突出用户关心的问题与已得到的关键结论：", sb.String())
	if err != nil || sum == "" {
		return
	}
	if uErr := h.chatService.UpdateSessionSummary(ctx, sessionID, sum); uErr != nil {
		klog.Warningf("[ChatHandler] 更新会话摘要失败: %v", uErr)
	}
}

// repoMemoryBlock 取本仓库其它会话的摘要，拼成「仓库历史记忆」系统提示段（L2 跨会话记忆）。
// memory.enabled 关闭或无可用摘要时返回空串。
func (h *ChatHandler) repoMemoryBlock(ctx context.Context, repoID uint, currentSessionID string) string {
	mc := config.GetConfig().Memory
	if !mc.Enabled {
		return ""
	}
	maxItems := mc.MaxItems
	if maxItems <= 0 {
		maxItems = 3
	}
	sessions, _, err := h.chatService.ListSessions(ctx, repoID, 0, 1, maxItems+5)
	if err != nil || len(sessions) == 0 {
		return ""
	}
	var sb strings.Builder
	n := 0
	for _, s := range sessions {
		if s.SessionID == currentSessionID || strings.TrimSpace(s.Summary) == "" {
			continue
		}
		if n == 0 {
			sb.WriteString("## 仓库历史记忆（本仓库其它会话的摘要，供参考，不一定与当前问题相关）\n")
		}
		sb.WriteString(fmt.Sprintf("- 〈%s〉%s\n", s.Title, s.Summary))
		n++
		if n >= maxItems {
			break
		}
	}
	return sb.String()
}

// ragBlock 对当前问题做语义检索，拼成「检索到的相关文档片段」系统提示段（L3 RAG）。
// embedding 关闭、检索出错或无命中时返回空串。
func (h *ChatHandler) ragBlock(ctx context.Context, repoID uint, query string) string {
	if h.ragService == nil || !h.ragService.Enabled() {
		return ""
	}
	chunks, err := h.ragService.Retrieve(ctx, repoID, query, config.GetConfig().Embedding.TopK)
	if err != nil {
		klog.Warningf("[ChatHandler] RAG 检索失败: %v", err)
		return ""
	}
	if len(chunks) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 检索到的相关文档片段（按相关度排序，供作答参考；如不相关可忽略）\n")
	for _, c := range chunks {
		sb.WriteString(fmt.Sprintf("- [DocID %d, 相关度 %.2f]\n%s\n\n", c.DocID, c.Score, c.Content))
	}
	return sb.String()
}

// runAgent 运行Agent
func (h *ChatHandler) runAgent(client *Client, userMsg *model.ChatMessage) {
	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 监听停止信号
	go func() {
		select {
		case <-client.stopChan:
			cancel()
		case <-ctx.Done():
			return
		}
	}()

	// 获取仓库信息
	var repoInfo string
	if h.repoService != nil {
		repo, err := h.repoService.Get(client.repoID)
		if err == nil && repo != nil {
			repoInfo = fmt.Sprintf("## 当前仓库信息\n- 仓库名称: %s\n- 仓库地址: %s\n- 本地路径: %s\n- 仓库描述: %s\n- 当前分支: %s\n- 当前Commit: %s\n",
				repo.Name, repo.URL, repo.LocalPath, repo.Description, repo.CloneBranch, repo.CloneCommit)

			// 追加文档列表供智能体查阅
			if h.docService != nil {
				docs, err := h.docService.GetByRepository(client.repoID)
				if err == nil && len(docs) > 0 {
					repoInfo += "\n## 文档列表\n"
					for _, doc := range docs {
						repoInfo += fmt.Sprintf("- 标题: %s, DocID: %d\n", doc.Title, doc.ID)
					}
					repoInfo += "\n可根据原始文档DocID，通过read_doc(doc_id)获取原文全文\n"
				}
			}
		}
	}

	// 若会话绑定了焦点文档（"就此文档提问"），在 system prompt 顶部前置焦点段；
	// agent 仍能看到全 repo 文档列表作 fallback，但被明确指引优先 read_doc(focusDocID)。
	if session, sErr := h.chatService.GetSession(ctx, client.sessionID); sErr == nil && session != nil && session.DocID > 0 {
		if h.docService != nil {
			if focusDoc, dErr := h.docService.Get(session.DocID); dErr == nil && focusDoc != nil {
				focusBlock := fmt.Sprintf(
					"## 焦点文档（本会话仅围绕此文档作答）\n- DocID: %d\n- 标题: %s\n\n请优先调用 read_doc(%d) 读取该文档完整内容回答用户问题；除非用户明确转向其它话题，不要主动检索或引用仓库内其它文档。\n\n",
					focusDoc.ID, focusDoc.Title, focusDoc.ID,
				)
				repoInfo = focusBlock + repoInfo
			}
		}
	}

	// L2：注入本仓库其它会话的历史摘要作为跨会话记忆
	if mem := h.repoMemoryBlock(ctx, client.repoID, client.sessionID); mem != "" {
		repoInfo += "\n" + mem
	}

	// L3：RAG 语义检索，注入与当前问题相关的文档片段
	if rag := h.ragBlock(ctx, client.repoID, userMsg.Content); rag != "" {
		repoInfo += "\n" + rag
	}

	// 获取 Agent
	agent, err := h.agentFactory.GetAgent("chat_assistant")
	if err != nil {
		client.sendError("AGENT_NOT_FOUND", fmt.Sprintf("无法获取Agent: %v", err))
		return
	}

	// 创建AI消息记录
	assistantMsg, err := h.chatService.CreateAssistantMessage(ctx, client.sessionID)
	if err != nil {
		client.sendError("INTERNAL_ERROR", "创建AI消息失败")
		return
	}

	// 发送 thinking_start 事件通知前端开始处理
	client.sendEvent(ServerMessage{
		Type:      "thinking_start",
		ID:        generateEventID(),
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]interface{}{
			"message_id": assistantMsg.MessageID,
		},
	})

	// 构建ADK消息列表
	var adkMessages []*schema.Message

	// 添加系统消息（仓库信息）
	if repoInfo != "" {
		adkMessages = append(adkMessages, &schema.Message{
			Role:    schema.System,
			Content: repoInfo,
		})
	}

	// 历史消息：按 token 预算装配为时间正序，超出预算的更早消息压成一段摘要。
	// （ListMessages 返回 created_at DESC，buildHistoryMessages 内部已反转为正序，
	//  并跳过本轮刚创建的 assistant 占位消息，保证对话以用户消息结尾。）
	adkMessages = append(adkMessages, h.buildHistoryMessages(ctx, client.sessionID, assistantMsg.MessageID)...)

	// 创建Runner并执行 — 按当前最高优先级 enabled 的 api_key 的 provider 决定是否启用流式
	//
	// 历史背景：流式之前因为走 OpenAI 兼容协议(中继 Bedrock claude→OpenAI 转换)在并行 tool_calls
	// 的 chunk 上把 Index 都标成 0，导致 Eino concatToolCalls 把不同 tool 的 args 错拼成一团，
	// 因此关掉。改用 Anthropic 原生协议(provider=anthropic)后，原生消息流里每个 content_block
	// 都带正确的 block_index，并行 tool 调用可以正确区分，流式重新可用。
	//
	// 但 OpenAI 兼容协议(GLM/DeepSeek 等)走 eino agent runner + EnableStreaming + tool-calling
	// 这条具体组合时，content delta chunk 会被重复 yield（用户看到"让我让我先先了解一下了解一下"
	// 这种逐 chunk 翻倍的现象）。eino 适配层 bug，未上游修复前不能对非 anthropic provider 启用
	// EnableStreaming，否则就是错误内容。
	//
	// 因此这里在创建 runner 前查一下当前会被 ProxyChatModel 选中的最高优先级 enabled api_key,
	// 仅当其 provider == "anthropic" 时才启用流式；其他 provider 走非流式分支（损失打字机 UX，
	// 但内容正确）。
	enableStreaming := false
	if h.apiKeyRepo != nil {
		if topKey, kerr := h.apiKeyRepo.GetHighestPriority(ctx); kerr == nil && topKey != nil && topKey.Provider == "anthropic" {
			enableStreaming = true
		}
	}
	klog.V(6).Infof("[ChatHandler] runAgent: EnableStreaming=%v", enableStreaming)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: enableStreaming})
	iter := runner.Run(ctx, adkMessages)

	var fullContent string
	var tokenUsed int
	// 追踪已发送的 content，避免重复
	sentContents := make(map[string]bool)
	// 追踪最近一个事件的 raw content，用于"同一回合的累积扩展" prefix 检测
	// (跨回合后 fullContent 含多回合拼接，无法判断当前回合的累积关系)
	var lastEventContent string

	// 遍历事件流
	for {
		select {
		case <-ctx.Done():
			// 用户取消或超时
			h.chatService.FinalizeMessage(ctx, assistantMsg.MessageID, tokenUsed, "stopped")
			client.sendEvent(ServerMessage{
				Type:      "stopped",
				ID:        generateEventID(),
				Timestamp: time.Now().UnixMilli(),
				Payload: map[string]interface{}{
					"message_id": assistantMsg.MessageID,
					"reason":     "user_cancelled",
				},
			})
			return
		default:
		}

		event, ok := iter.Next()
		if !ok {
			break
		}

		if event.Err != nil {
			// 执行出错
			h.chatService.FinalizeMessage(ctx, assistantMsg.MessageID, tokenUsed, "error")
			client.sendError("AGENT_ERROR", event.Err.Error())
			return
		}

		// 处理输出事件
		if event.Output != nil && event.Output.MessageOutput != nil {
			// 跳过 role="tool" 的系统消息（不发送给前端，也不存数据库）
			if event.Output.MessageOutput.Role == "tool" {
				klog.V(6).Info("跳过 role=tool 的系统消息，不发送给前端")
				continue
			}

			// 流式分支：逐 chunk 推送 delta，让前端有真正的"打字机"体验
			if event.Output.MessageOutput.IsStreaming && event.Output.MessageOutput.MessageStream != nil {
				stream := event.Output.MessageOutput.MessageStream
				var streamFullContent string
				var collectedToolCalls []schema.ToolCall
				for {
					chunk, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						klog.ErrorS(err, "MessageStream.Recv 失败")
						break
					}
					if chunk == nil {
						continue
					}
					// 累积 tool_calls（每个 chunk 可能携带工具调用增量片段，最终一起处理）
					if len(chunk.ToolCalls) > 0 {
						collectedToolCalls = append(collectedToolCalls, chunk.ToolCalls...)
					}
					delta := chunk.Content
					if delta == "" {
						continue
					}
					// 流式增量发送
					client.sendEvent(ServerMessage{
						Type:      "content_delta",
						ID:        assistantMsg.MessageID,
						Timestamp: time.Now().UnixMilli(),
						Payload: map[string]interface{}{
							"message_id": assistantMsg.MessageID,
							"delta":      delta,
						},
					})
					streamFullContent += delta
					fullContent += delta
					// 周期性写库（避免每 chunk 都写）—— 简单起见这里每块都写，
					// 因为 SQLite 单写者足够 chunk 频率
					h.chatService.UpdateMessageContent(ctx, assistantMsg.MessageID, fullContent)
				}
				stream.Close()

				// 把流式收完的整段内容登记到去重表 ——
				// ADK runner 在 EnableStreaming=true 下，可能在流结束后又发一个 IsStreaming=false
				// 的 consolidated 事件（同样的内容整段重发），它原本是给非流式 caller 的便利，
				// 我们已经在 streaming 分支处理过了，必须在非流式分支跳掉，否则内容会写两份。
				if streamFullContent != "" {
					sentContents[streamFullContent] = true
				}

				// chat_assistant agent 约定最终答案以 </final> 结束，</final> 之后不应再有任何字符。
				// ADK runner 在 EnableStreaming 模式偶尔会在流结束后再发一个等价 stream/consolidated 事件 ——
				// 用 </final> 作为业务级"我说完了"信号提前 break，可以彻底规避内容被写两遍的问题。
				if strings.Contains(streamFullContent, "</final>") {
					goto streamingDone
				}

				// 流结束后批量发出工具调用事件
				if len(collectedToolCalls) > 0 {
					// 同 ID 的 tool_call 在不同 chunk 上可能被切分（function.Arguments 为 JSON 流片段），
					// 通过 ID 合并 arguments
					merged := make(map[string]*schema.ToolCall)
					order := make([]string, 0, len(collectedToolCalls))
					for i := range collectedToolCalls {
						tc := collectedToolCalls[i]
						if existing, ok := merged[tc.ID]; ok {
							existing.Function.Arguments += tc.Function.Arguments
							if existing.Function.Name == "" {
								existing.Function.Name = tc.Function.Name
							}
						} else {
							copy := tc
							merged[tc.ID] = &copy
							order = append(order, tc.ID)
						}
					}
					for _, id := range order {
						tc := merged[id]
						client.sendEvent(ServerMessage{
							Type:      "tool_call",
							ID:        generateEventID(),
							Timestamp: time.Now().UnixMilli(),
							Payload: map[string]interface{}{
								"tool_call_id": tc.ID,
								"tool_name":    tc.Function.Name,
								"arguments":    tc.Function.Arguments,
							},
						})
						h.chatService.CreateOrUpdateToolCall(ctx, assistantMsg.MessageID, tc.ID, tc.Function.Name, tc.Function.Arguments)
					}
				}
			} else {
				// 非流式分支（保留原逻辑作为兜底）
				//
				// 早期版本会把"未以 <final>/<thinking> 开头"的 assistant 内容
				// 强行包一层 <thinking>...</thinking>，假设模型一定按 chat_assistant agent
				// 协议输出 <final>...</final> 终段。但 GLM/DeepSeek/Qwen 等 OpenAI-compat
				// 模型并不可靠遵循这个内部协议，会直接吐 markdown 答案。强行包 thinking 后
				// 前端 splitThinkingFinal 提取不到 final 块，整段答案被收进可折叠的"思考过程"
				// 里，用户感觉没收到回答。所以改为不再强行包裹，让前端 fallback 路径
				// （!thinking && !final 时直接渲染原 content）兜住未打标的回答。
				content := event.Output.MessageOutput.Message.Content
				if content != "" && !sentContents[content] {
					sentContents[content] = true

					// ADK runner 对同一回合可能先发一个中间态 MessageOutput，再发一个
					// consolidated MessageOutput；不同回合（如 tool 调用前后）也会各发一次。
					// 三种情况：
					//   1. 当前 content 是 lastEventContent 的扩展（同回合累积）→ 只发新增尾部
					//   2. 当前 content 是 lastEventContent 的子串（同回合旧片段）→ 跳过
					//   3. 其他 → 新回合内容，整段附加
					var delta string
					switch {
					case lastEventContent != "" && strings.HasPrefix(content, lastEventContent):
						delta = content[len(lastEventContent):]
					case lastEventContent != "" && strings.Contains(lastEventContent, content):
						delta = ""
					default:
						delta = content
					}
					lastEventContent = content

					if delta != "" {
						client.sendEvent(ServerMessage{
							Type:      "content_delta",
							ID:        assistantMsg.MessageID,
							Timestamp: time.Now().UnixMilli(),
							Payload: map[string]interface{}{
								"message_id": assistantMsg.MessageID,
								"delta":      delta,
							},
						})
						fullContent += delta
						h.chatService.UpdateMessageContent(ctx, assistantMsg.MessageID, fullContent)
					}
				}

				// 处理工具调用
				if len(event.Output.MessageOutput.Message.ToolCalls) > 0 {
					for _, tc := range event.Output.MessageOutput.Message.ToolCalls {
						// 发送工具调用事件
						client.sendEvent(ServerMessage{
							Type:      "tool_call",
							ID:        generateEventID(),
							Timestamp: time.Now().UnixMilli(),
							Payload: map[string]interface{}{
								"tool_call_id": tc.ID,
								"tool_name":    tc.Function.Name,
								"arguments":    tc.Function.Arguments,
							},
						})

						// 保存工具调用到数据库
						h.chatService.CreateOrUpdateToolCall(ctx, assistantMsg.MessageID, tc.ID, tc.Function.Name, tc.Function.Arguments)
					}
				}
			}
		}

		// 检查是否退出
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
streamingDone:
	// 发送后清空去重 map，避免无限增长
	sentContents = make(map[string]bool)
	// 完成消息
	h.chatService.FinalizeMessage(ctx, assistantMsg.MessageID, tokenUsed, "completed")

	// L2：异步刷新会话摘要，沉淀为跨会话记忆
	go h.maybeUpdateSessionSummary(client.sessionID)

	// 发送 assistant_end 事件通知前端
	client.sendEvent(ServerMessage{
		Type:      "assistant_end",
		ID:        generateEventID(),
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]interface{}{
			"message_id": assistantMsg.MessageID,
			"token_used": tokenUsed,
		},
	})
}

// sendEvent 发送事件（线程安全）
func (c *Client) sendEvent(event ServerMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return // 连接已关闭，不发送
	}

	data, _ := json.Marshal(event)
	select {
	case c.send <- data:
	default:
		// 发送通道已满，丢弃消息
	}
}

// generateEventID 生成事件ID
func generateEventID() string {
	return fmt.Sprintf("evt_%d", time.Now().UnixNano())
}

// ClientMessage 客户端消息
type ClientMessage struct {
	Type    string `json:"type"`              // message, stop, ping
	Content string `json:"content,omitempty"` // type=message时使用
	ID      string `json:"id"`
}

// ServerMessage 服务端消息
type ServerMessage struct {
	Type      string      `json:"type"`
	ID        string      `json:"id"`
	Timestamp int64       `json:"timestamp"`
	Payload   interface{} `json:"payload,omitempty"`
}

// ErrorPayload 错误载荷
type ErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
