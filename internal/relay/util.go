package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (u Upstream) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return http.DefaultClient
}

// doJSON 发送一次 JSON 请求，非 2xx 时返回带响应体的 UpstreamError。
func doJSON(ctx context.Context, up Upstream, method, url string, headers map[string]string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(withContext(ctx), method, url, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "*/*")
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := up.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		detail := upstreamErrorMessage(string(raw))
		if detail == "" {
			detail = truncate(string(raw), 400)
		}
		return nil, &UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       string(raw),
			Message:    fmt.Sprintf("上游返回 %d：%s", resp.StatusCode, detail),
		}
	}
	return resp, nil
}

func decodeJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("上游响应不是合法 JSON：%s", truncate(string(raw), 300))
	}
	return nil
}

func decodeJSONBytes(resp *http.Response) (map[string]any, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("上游响应不是合法 JSON：%s", truncate(string(raw), 300))
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------- SSE ----------

// sseEvent 是一次 SSE 事件；data 已去掉 "data: " 前缀。
type sseEvent struct {
	Event string
	Data  string
}

type sseReader struct {
	sc *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	// 上游单条事件可能很大（base64 图片），放宽缓冲上限。
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &sseReader{sc: sc}
}

// Next 读取下一条 SSE 事件；ok=false 表示流结束。
func (s *sseReader) Next() (ev sseEvent, ok bool, err error) {
	var event strings.Builder
	var data strings.Builder
	for s.sc.Scan() {
		line := s.sc.Text()
		if line == "" {
			if data.Len() > 0 {
				return sseEvent{Event: event.String(), Data: strings.TrimSuffix(data.String(), "\n")}, true, nil
			}
			event.Reset()
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			event.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			data.WriteString("\n")
		}
	}
	if err := s.sc.Err(); err != nil {
		return sseEvent{}, false, err
	}
	if data.Len() > 0 {
		return sseEvent{Event: event.String(), Data: strings.TrimSuffix(data.String(), "\n")}, true, nil
	}
	return sseEvent{}, false, nil
}

// ---------- JSON 路径取值 ----------

// jsonPath 支持 "data.task_id"、"data.outputs[0].url"、"[0].url"、"$" 前缀。
func jsonPath(root any, path string) any {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return root
	}
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	cur := root
	for _, seg := range splitPath(path) {
		if seg == "" {
			continue
		}
		name := seg
		idx := -1
		if i := strings.IndexByte(seg, '['); i >= 0 {
			name = seg[:i]
			rest := strings.TrimSuffix(seg[i:], "]")
			rest = strings.TrimPrefix(rest, "[")
			if n, err := strconv.Atoi(rest); err == nil {
				idx = n
			}
		}
		if name != "" {
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur, ok = obj[name]
			if !ok {
				return nil
			}
		}
		if idx >= 0 {
			arr, ok := cur.([]any)
			if !ok || idx >= len(arr) {
				return nil
			}
			cur = arr[idx]
		}
	}
	return cur
}

func splitPath(path string) []string {
	var out []string
	var buf strings.Builder
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '.':
			out = append(out, buf.String())
			buf.Reset()
		case '[':
			out = append(out, buf.String())
			buf.Reset()
			j := i + 1
			for j < len(path) && path[j] != ']' {
				j++
			}
			out = append(out, "["+path[i+1:min(j, len(path))]+"]")
			i = j
		default:
			buf.WriteByte(path[i])
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

func jsonPathString(root any, path string) string {
	v := jsonPath(root, path)
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	case map[string]any:
		// 部分上游把错误写成对象（如 {"message": "..."}），这里取其中的可读信息。
		return firstNonEmpty(
			asString(t["message"]), asString(t["msg"]),
			asString(t["detail"]), asString(t["error"]))
	}
	return ""
}

func jsonPathInt(root any, path string) int {
	v := jsonPath(root, path)
	switch t := v.(type) {
	case float64:
		return int(t)
	case int64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

// upstreamErrorMessage 从上游错误响应里提取可读信息，避免把整段 JSON 抛给用户。
// 依次兼容：OpenAI 的 error.message、FastAPI 风格的 detail（字符串或校验数组），
// 以及常见的 message / msg 字段；都取不到时返回空串，由调用方回退到原始报文。
func upstreamErrorMessage(raw string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return ""
	}
	if e, ok := obj["error"].(map[string]any); ok {
		if m := asString(e["message"]); m != "" {
			return m
		}
	}
	if m := asString(obj["error"]); m != "" {
		return m
	}
	for _, key := range []string{"detail", "message", "msg", "error_message", "reason"} {
		switch v := obj[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []any:
			// FastAPI 的校验错误形如 [{"loc":[...],"msg":"..."}]，拼成一句话。
			msgs := make([]string, 0, len(v))
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if s := asString(m["msg"]); s != "" {
						msgs = append(msgs, s)
					}
				}
			}
			if len(msgs) > 0 {
				return strings.Join(msgs, "；")
			}
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
