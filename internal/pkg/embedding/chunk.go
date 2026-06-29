package embedding

import "strings"

// Chunk 把 Markdown 文本切成不超过 maxChars（按 rune 计）的片段。
// 优先按标题行切分成 section，过大的 section 再按长度硬切。空内容返回 nil。
func Chunk(content string, maxChars int) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if maxChars <= 0 {
		maxChars = 1200
	}

	// 1) 按标题行切分成 section
	lines := strings.Split(content, "\n")
	var sections []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			sections = append(sections, s)
		}
		cur.Reset()
	}
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") && cur.Len() > 0 {
			flush()
		}
		cur.WriteString(ln)
		cur.WriteString("\n")
	}
	flush()

	// 2) 对超长 section 按 maxChars 硬切（按 rune，避免截断多字节字符）
	var chunks []string
	for _, sec := range sections {
		r := []rune(sec)
		if len(r) <= maxChars {
			chunks = append(chunks, sec)
			continue
		}
		for i := 0; i < len(r); i += maxChars {
			end := i + maxChars
			if end > len(r) {
				end = len(r)
			}
			if s := strings.TrimSpace(string(r[i:end])); s != "" {
				chunks = append(chunks, s)
			}
		}
	}
	return chunks
}
