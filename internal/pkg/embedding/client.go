package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"gitee.com/li-yuyanglee/leelens-backend/config"
	"gitee.com/li-yuyanglee/leelens-backend/internal/model"
	"gitee.com/li-yuyanglee/leelens-backend/internal/repository"
)

// Embedder 把文本批量转成向量。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	ModelName() string
	Dimension() int
}

// NewEmbedder 按配置构造 embedder。
// provider == "mock" 时返回确定性占位向量（仅供链路验证，召回无语义意义）。
// provider 为空或 "openai" 时走 OpenAI 兼容 /embeddings 端点，复用数据库里的 api_key。
func NewEmbedder(cfg config.EmbeddingConfig, apiKeyRepo repository.APIKeyRepository) (Embedder, error) {
	dim := cfg.Dimension
	if dim <= 0 {
		dim = 1536
	}
	switch cfg.Provider {
	case "mock":
		return &mockEmbedder{dim: dim, model: orDefault(cfg.Model, "mock-embedding")}, nil
	case "", "openai":
		return &openAIEmbedder{
			cfg:        cfg,
			apiKeyRepo: apiKeyRepo,
			dim:        dim,
			client:     &http.Client{Timeout: 2 * time.Minute},
		}, nil
	default:
		return nil, fmt.Errorf("不支持的 embedding provider: %s", cfg.Provider)
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---- mock embedder（确定性占位向量）----

type mockEmbedder struct {
	dim   int
	model string
}

func (m *mockEmbedder) ModelName() string { return m.model }
func (m *mockEmbedder) Dimension() int    { return m.dim }
func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = hashVector(t, m.dim)
	}
	return out, nil
}

// hashVector 用内容 hash 生成确定性单位向量：相同文本→相同向量，便于验证存取/检索链路。
func hashVector(text string, dim int) []float32 {
	v := make([]float32, dim)
	var norm float64
	block := sha256.Sum256([]byte(text))
	bi, counter := 0, 0
	for i := 0; i < dim; i++ {
		if bi+4 > len(block) {
			counter++
			block = sha256.Sum256(append([]byte(text), byte(counter)))
			bi = 0
		}
		u := binary.LittleEndian.Uint32(block[bi : bi+4])
		bi += 4
		f := float32(u)/float32(math.MaxUint32)*2 - 1 // 映射到 [-1, 1]
		v[i] = f
		norm += float64(f) * float64(f)
	}
	if norm > 0 {
		n := float32(math.Sqrt(norm))
		for i := range v {
			v[i] /= n
		}
	}
	return v
}

// ---- OpenAI 兼容 embedder ----

type openAIEmbedder struct {
	cfg        config.EmbeddingConfig
	apiKeyRepo repository.APIKeyRepository
	dim        int
	client     *http.Client
}

func (e *openAIEmbedder) ModelName() string {
	if e.cfg.Model != "" {
		return e.cfg.Model
	}
	return "embedding"
}
func (e *openAIEmbedder) Dimension() int { return e.dim }

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	key, err := e.pickKey(ctx)
	if err != nil {
		return nil, err
	}
	model := e.cfg.Model
	if model == "" {
		model = key.Model
	}

	reqBody, _ := json.Marshal(map[string]any{"model": model, "input": texts})
	url := strings.TrimRight(key.BaseURL, "/") + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key.APIKey)

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embeddings 请求失败: status=%d body=%s", resp.StatusCode, string(b))
	}

	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	return out, nil
}

func (e *openAIEmbedder) pickKey(ctx context.Context) (*model.APIKey, error) {
	if e.cfg.APIKeyName != "" {
		k, err := e.apiKeyRepo.GetByName(ctx, e.cfg.APIKeyName)
		if err != nil {
			return nil, err
		}
		if k == nil {
			return nil, fmt.Errorf("未找到名为 %q 的 api_key（embedding.api_key_name）", e.cfg.APIKeyName)
		}
		return k, nil
	}
	k, err := e.apiKeyRepo.GetHighestPriority(ctx)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, fmt.Errorf("没有可用的 api_key 供 embeddings 使用")
	}
	return k, nil
}
