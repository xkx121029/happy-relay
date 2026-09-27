package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// errNoUpstream 表示模型未绑定任何可用渠道/入口。
var errNoUpstream = errors.New("模型未绑定任何可用渠道或入口地址")

// probeClient 供渠道探测、健康巡检与 /v1 透传共用的连接池客户端。
// 不设 Client.Timeout：各调用方按需用 request context 控制单次超时，
// 这样客户端可全局共享、keep-alive 连接得以复用。
var probeClient = &http.Client{Transport: relay.PooledTransport()}

// resolveUpstream 把对外模型名解析成一个具体的上游调用地址（入口 + 密钥）。
func (s *Server) resolveUpstream(model string) (relay.Upstream, store.Model, error) {
	m, ps, err := s.eng.ResolveAll(model)
	if err != nil {
		return relay.Upstream{}, m, err
	}
	if len(ps) == 0 {
		return relay.Upstream{}, m, errNoUpstream
	}
	ups := s.eng.Upstreams(ps[0])
	if len(ups) == 0 {
		return relay.Upstream{}, m, errNoUpstream
	}
	return ups[0], m, nil
}

// proxySupported 仅对 OpenAI 兼容 / 智谱上游开放透传接口。
func proxySupported(kind string) bool {
	return kind == store.KindOpenAI || kind == store.KindZhipu
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request, key store.APIKey) {
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
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	up, m, err := s.resolveUpstream(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	if !proxySupported(m.ProviderKind) {
		writeErr(w, http.StatusNotImplemented, "unsupported", "该模型的上游类型不支持 /v1/responses 透传")
		return
	}
	raw["model"] = m.UpstreamName
	s.doProxy(w, r, up, strings.TrimRight(up.BaseURL, "/")+"/responses", raw)
}

func (s *Server) handleBatchesList(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.proxyBatches(w, r, key, "")
}

func (s *Server) handleBatchesCreate(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.proxyBatches(w, r, key, "")
}

func (s *Server) handleBatchesGet(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.proxyBatches(w, r, key, "/"+r.PathValue("id"))
}

func (s *Server) handleBatchesCancel(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.proxyBatches(w, r, key, "/"+r.PathValue("id")+"/cancel")
}

func (s *Server) handleBatchesContent(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.proxyBatches(w, r, key, "/"+r.PathValue("id")+"/content")
}

// proxyBatches 透传 OpenAI Batch 接口。模型由请求体（创建）或 ?model= 查询参数决定，
// 因为按 id 查询时上游信息不在请求里。非 OpenAI 兼容上游返回 501。
func (s *Server) proxyBatches(w http.ResponseWriter, r *http.Request, key store.APIKey, suffix string) {
	model := r.URL.Query().Get("model")
	var body map[string]any
	if r.Method == http.MethodPost {
		raw, err := decodeMap(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
			return
		}
		if m, ok := raw["model"].(string); ok && m != "" {
			model = m
		}
		body = raw
	}
	if strings.TrimSpace(model) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "批量接口需提供 model（创建时放请求体，查询时放 ?model=）")
		return
	}
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	up, m, err := s.resolveUpstream(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	if !proxySupported(m.ProviderKind) {
		writeErr(w, http.StatusNotImplemented, "unsupported", "该模型的上游类型不支持 /v1/batches 透传")
		return
	}
	if body != nil {
		body["model"] = m.UpstreamName
	}
	s.doProxy(w, r, up, strings.TrimRight(up.BaseURL, "/")+"/batches"+suffix, body)
}

// doProxy 把请求透明转发到上游：保留方法，POST 时携带 JSON 请求体，
// 透传鉴权与自定义头；流式响应直接管道转发，否则原样回写状态码与内容。
func (s *Server) doProxy(w http.ResponseWriter, r *http.Request, up relay.Upstream, target string, body map[string]any) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	var req *http.Request
	var err error
	if len(payload) > 0 {
		req, err = http.NewRequest(r.Method, target, bytes.NewReader(payload))
	} else {
		req, err = http.NewRequest(r.Method, target, nil)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	timeout := time.Duration(max(s.cfg.TimeoutSec, 60)) * time.Second
	cctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	req = req.WithContext(cctx)
	req.Header.Set("Content-Type", "application/json")
	if up.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+up.APIKey)
	}
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}

	resp, err := probeClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	streaming := body != nil && body["stream"] == true
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
