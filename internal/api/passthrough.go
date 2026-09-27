package api

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// ---------- 出站透传：更多 OpenAI 兼容端点 ----------
//
// 现有适配器覆盖 chat/completions/embeddings/images/videos/audio 等主能力，
// 这里补齐低频但常用的端点，统一走「解析模型 → 重写为上游模型名 → 透传」：
//   - JSON 端点（moderations / rerank）：请求体带 model，解码重写后转发；
//   - multipart 端点（audio/translations、images/edits、images/variations）：
//     重写表单里的 model 字段后整体重建转发；
//   - 文件端点（/v1/files*）：文件本身不属于某个模型，路由渠道由 ?model=
//     查询参数指定（与 /v1/batches 的约定一致），请求体原样转发。
//
// 透传不落调用日志（与 /v1/responses、/v1/batches 保持一致）。

// passthroughTimeout 透传请求的兜底超时，与 doProxy 一致。
func (s *Server) passthroughTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	timeout := time.Duration(max(s.cfg.TimeoutSec, 60)) * time.Second
	return context.WithTimeout(r.Context(), timeout)
}

// buildUpstreamRequest 构造发往上游的请求：叠加兜底超时、鉴权与渠道自定义头。
func (s *Server) buildUpstreamRequest(r *http.Request, up relay.Upstream, method, target string, body []byte, contentType string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	ctx, cancel := s.passthroughTimeout(r)
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		cancel()
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if up.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+up.APIKey)
	}
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// forwardResponse 把上游响应回写给客户端：原样透传状态码与内容。
func forwardResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// resolveForPassthrough 完成「模型名 → 上游」的公共前置校验：
// 秘钥授权、渠道解析与上游协议支持检查。返回可直接使用的上游信息。
func (s *Server) resolveForPassthrough(w http.ResponseWriter, key store.APIKey, model, endpoint string) (relay.Upstream, store.Model, bool) {
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return relay.Upstream{}, store.Model{}, false
	}
	up, m, err := s.resolveUpstream(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return relay.Upstream{}, store.Model{}, false
	}
	if !proxySupported(m.ProviderKind) {
		writeErr(w, http.StatusNotImplemented, "unsupported", "该模型的上游类型不支持 "+endpoint+" 透传")
		return relay.Upstream{}, store.Model{}, false
	}
	return up, m, true
}

// ---------- JSON 端点：moderations / rerank ----------

func (s *Server) handleModerations(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.jsonModelPassthrough(w, r, key, "/moderations")
}

func (s *Server) handleRerank(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.jsonModelPassthrough(w, r, key, "/rerank")
}

// jsonModelPassthrough 透传「请求体带 model 的 JSON 端点」。
func (s *Server) jsonModelPassthrough(w http.ResponseWriter, r *http.Request, key store.APIKey, path string) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	model, _ := raw["model"].(string)
	if strings.TrimSpace(model) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 model")
		return
	}
	up, m, ok := s.resolveForPassthrough(w, key, model, path)
	if !ok {
		return
	}
	raw["model"] = m.UpstreamName
	s.doProxy(w, r, up, strings.TrimRight(up.BaseURL, "/")+path, raw)
}

// ---------- multipart 端点：audio/translations、images/edits、images/variations ----------

func (s *Server) handleAudioTranslations(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.multipartModelPassthrough(w, r, key, "/audio/translations")
}

func (s *Server) handleImageEdits(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.multipartModelPassthrough(w, r, key, "/images/edits")
}

func (s *Server) handleImageVariations(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.multipartModelPassthrough(w, r, key, "/images/variations")
}

// rewriteMultipartModel 解析 multipart 表单，把 model 字段替换为上游模型名，
// 其余字段与文件原样保留，返回可直发的请求体与对应 Content-Type。
func rewriteMultipartModel(r *http.Request, model string) ([]byte, string, error) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return nil, "", err
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	hasModel := false
	for k, vals := range r.MultipartForm.Value {
		for _, v := range vals {
			if k == "model" {
				if !hasModel {
					v = model
					hasModel = true
				} else {
					continue // 去掉重复的 model 字段
				}
			}
			if err := mw.WriteField(k, v); err != nil {
				return nil, "", err
			}
		}
	}
	if !hasModel {
		if err := mw.WriteField("model", model); err != nil {
			return nil, "", err
		}
	}
	for k, fds := range r.MultipartForm.File {
		for _, fd := range fds {
			fh, err := fd.Open()
			if err != nil {
				return nil, "", err
			}
			part, err := mw.CreateFormFile(k, fd.Filename)
			if err != nil {
				fh.Close()
				return nil, "", err
			}
			_, err = io.Copy(part, fh)
			fh.Close()
			if err != nil {
				return nil, "", err
			}
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), mw.FormDataContentType(), nil
}

// multipartModelPassthrough 透传「表单带 model 的 multipart 端点」。
func (s *Server) multipartModelPassthrough(w http.ResponseWriter, r *http.Request, key store.APIKey, path string) {
	model := r.FormValue("model")
	if strings.TrimSpace(model) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 model")
		return
	}
	up, m, ok := s.resolveForPassthrough(w, key, model, path)
	if !ok {
		return
	}
	payload, contentType, err := rewriteMultipartModel(r, m.UpstreamName)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "解析表单失败："+err.Error())
		return
	}
	req, err := s.buildUpstreamRequest(r, up, r.Method, strings.TrimRight(up.BaseURL, "/")+path, payload, contentType)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	forwardResponse(w, resp)
}

// ---------- 文件端点：/v1/files* ----------

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.filesPassthrough(w, r, key, "")
}

func (s *Server) handleFilesCreate(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.filesPassthrough(w, r, key, "")
}

func (s *Server) handleFilesGet(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.filesPassthrough(w, r, key, "/"+r.PathValue("id"))
}

func (s *Server) handleFilesDelete(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.filesPassthrough(w, r, key, "/"+r.PathValue("id"))
}

func (s *Server) handleFilesContent(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.filesPassthrough(w, r, key, "/"+r.PathValue("id")+"/content")
}

// filesPassthrough 透传 OpenAI Files 接口。文件不属于某个模型，
// 路由渠道由 ?model= 查询参数指定（与 /v1/batches 相同的约定），
// 请求体（multipart 上传 / 空体）原样转发，Content-Type 原样保留。
func (s *Server) filesPassthrough(w http.ResponseWriter, r *http.Request, key store.APIKey, suffix string) {
	model := r.URL.Query().Get("model")
	if strings.TrimSpace(model) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"文件接口需带 ?model= 指定路由渠道（文件不属于模型，中转站靠它选择上游）")
		return
	}
	up, _, ok := s.resolveForPassthrough(w, key, model, "/v1/files")
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "读取请求体失败："+err.Error())
		return
	}
	req, err := s.buildUpstreamRequest(r, up, r.Method,
		strings.TrimRight(up.BaseURL, "/")+"/files"+suffix, body, r.Header.Get("Content-Type"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	forwardResponse(w, resp)
}
