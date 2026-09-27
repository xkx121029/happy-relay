// Package api 提供两层 HTTP 接口：
//   - /v1/*  对外统一接口，需 Bearer 秘钥，供第三方客户端调用
//   - /api/* 面板管理接口，仅供自用网页面板调用
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"happy-relay/internal/config"
	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

type Server struct {
	cfg     *config.Config
	st      *store.Store
	eng     *relay.Engine
	static  fs.FS
	limiter *keyLimiter
	// cache 是统一的非流式响应缓存（按模型+请求体 hash）。
	cache *responseCache
	// mlimiter 是按对外模型的 RPM/TPM 滑动窗口限速器。
	mlimiter *modelLimiter
	// ag 是告警去抖闸门：按 scope（模型名）统计近期失败次数，达到阈值才推送。
	ag *alertGate
	// opts 缓存当前生效的运行参数，保存设置后热更新，读取无锁。
	opts atomic.Pointer[Options]
	// us 缓存渠道可用性集合（带 TTL），供模型列表过滤与引擎选路共用。
	us usableCache
}

func New(cfg *config.Config, st *store.Store, eng *relay.Engine, static fs.FS) *Server {
	s := &Server{
		cfg:      cfg,
		st:       st,
		eng:      eng,
		static:   static,
		limiter:  newKeyLimiter(),
		cache:    newResponseCache(),
		mlimiter: newModelLimiter(),
		ag:       newAlertGate(),
	}
	// 引擎选路时跳过「不可能调通」的渠道：没配密钥、连通巡检也没通过过的
	// 内置渠道。这让 Chat/auto 的故障转移不再把尝试浪费在注定 401 的渠道上。
	eng.UsableFilter = func(p store.Provider) bool {
		set, _, err := s.usableSnapshot()
		if err != nil {
			return true // 判定失败时不拦，保持可用
		}
		return set[p.ID]
	}
	s.reloadOptions()
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---------- 健康检查与运维 ----------
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	// ---------- 对外 v1 ----------
	mux.HandleFunc("GET /v1/models", s.withKey(s.handleModels))
	mux.HandleFunc("GET /v1/skills", s.withKey(s.handleSkillCatalog))
	mux.HandleFunc("POST /v1/chat/completions", s.withKey(s.handleChat))
	mux.HandleFunc("POST /v1/completions", s.withKey(s.handleLegacyCompletion))
	mux.HandleFunc("POST /v1/embeddings", s.withKey(s.handleEmbeddings))
	mux.HandleFunc("POST /v1/audio/speech", s.withKey(s.handleSpeech))
	mux.HandleFunc("POST /v1/audio/transcriptions", s.withKey(s.handleTranscription))
	mux.HandleFunc("POST /v1/images/generations", s.withKey(s.handleImage))
	mux.HandleFunc("GET /v1/videos", s.withKey(s.handleVideoList))
	mux.HandleFunc("POST /v1/videos", s.withKey(s.handleVideoCreate))
	mux.HandleFunc("GET /v1/videos/{id}", s.withKey(s.handleVideoGet))
	mux.HandleFunc("DELETE /v1/videos/{id}", s.withKey(s.handleVideoDelete))
	mux.HandleFunc("GET /v1/videos/{id}/stream", s.withKey(s.handleVideoStream))
	// 兼容以 /generations 结尾的创建写法（查询统一走 /v1/videos/{id}）
	mux.HandleFunc("POST /v1/videos/generations", s.withKey(s.handleVideoCreate))

	// ---------- 透传类接口（仅 OpenAI 兼容上游） ----------
	mux.HandleFunc("POST /v1/responses", s.withKey(s.handleResponses))
	mux.HandleFunc("GET /v1/batches", s.withKey(s.handleBatchesList))
	mux.HandleFunc("POST /v1/batches", s.withKey(s.handleBatchesCreate))
	mux.HandleFunc("GET /v1/batches/{id}", s.withKey(s.handleBatchesGet))
	mux.HandleFunc("POST /v1/batches/{id}/cancel", s.withKey(s.handleBatchesCancel))
	mux.HandleFunc("GET /v1/batches/{id}/content", s.withKey(s.handleBatchesContent))
	// 内容审核与重排序
	mux.HandleFunc("POST /v1/moderations", s.withKey(s.handleModerations))
	mux.HandleFunc("POST /v1/rerank", s.withKey(s.handleRerank))
	// 语音翻译与图片编辑（multipart 表单）
	mux.HandleFunc("POST /v1/audio/translations", s.withKey(s.handleAudioTranslations))
	mux.HandleFunc("POST /v1/images/edits", s.withKey(s.handleImageEdits))
	mux.HandleFunc("POST /v1/images/variations", s.withKey(s.handleImageVariations))
	// 文件管理（路由渠道由 ?model= 指定）
	mux.HandleFunc("GET /v1/files", s.withKey(s.handleFilesList))
	mux.HandleFunc("POST /v1/files", s.withKey(s.handleFilesCreate))
	mux.HandleFunc("GET /v1/files/{id}", s.withKey(s.handleFilesGet))
	mux.HandleFunc("DELETE /v1/files/{id}", s.withKey(s.handleFilesDelete))
	mux.HandleFunc("GET /v1/files/{id}/content", s.withKey(s.handleFilesContent))

	// ---------- 面板管理 ----------
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/backup", s.handleBackupExport)
	mux.HandleFunc("POST /api/backup", s.handleBackupImport)

	mux.HandleFunc("GET /api/providers", s.handleProviderList)
	mux.HandleFunc("POST /api/providers", s.handleProviderSave)
	mux.HandleFunc("PUT /api/providers/{id}", s.handleProviderSave)
	mux.HandleFunc("DELETE /api/providers/{id}", s.handleProviderDelete)
	mux.HandleFunc("POST /api/providers/test", s.handleProviderTest)
	mux.HandleFunc("GET /api/providers/health", s.handleProviderHealth)
	mux.HandleFunc("POST /api/providers/reset", s.handleProviderResetAll)
	mux.HandleFunc("POST /api/providers/{id}/reset", s.handleProviderReset)

	mux.HandleFunc("GET /api/catalog", s.handleCatalogList)
	mux.HandleFunc("POST /api/catalog", s.handleCatalogSave)
	mux.HandleFunc("PUT /api/catalog/{id}", s.handleCatalogSave)
	mux.HandleFunc("DELETE /api/catalog/{id}", s.handleCatalogDelete)
	mux.HandleFunc("POST /api/catalog/import", s.handleCatalogImport)

	mux.HandleFunc("GET /api/models", s.handleModelList)
	mux.HandleFunc("POST /api/models", s.handleModelSave)
	mux.HandleFunc("POST /api/models/preset", s.handleModelFromPreset)
	mux.HandleFunc("POST /api/models/auto-aggregate", s.handleAutoAggregate)
	mux.HandleFunc("POST /api/models/ai-create", s.handleModelAICreate)
	mux.HandleFunc("POST /api/agent/chat", s.handleAgentChat)
	mux.HandleFunc("GET /api/stats/upstreams", s.handleUpstreamStats)
	mux.HandleFunc("POST /api/models/benchmark", s.handleModelBenchmark)
	mux.HandleFunc("PUT /api/models/{id}", s.handleModelSave)
	mux.HandleFunc("DELETE /api/models/{id}", s.handleModelDelete)

	mux.HandleFunc("GET /api/keys", s.handleKeyList)
	mux.HandleFunc("POST /api/keys", s.handleKeySave)
	mux.HandleFunc("PUT /api/keys/{id}", s.handleKeySave)
	mux.HandleFunc("DELETE /api/keys/{id}", s.handleKeyDelete)

	mux.HandleFunc("GET /api/skills", s.handleSkillList)
	mux.HandleFunc("POST /api/skills", s.handleSkillSave)
	mux.HandleFunc("PUT /api/skills/{id}", s.handleSkillSave)
	mux.HandleFunc("DELETE /api/skills/{id}", s.handleSkillDelete)
	mux.HandleFunc("POST /api/skills/generate", s.handleSkillGenerate)

	mux.HandleFunc("GET /api/tasks", s.handleTaskList)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleTaskDelete)

	mux.HandleFunc("GET /api/logs", s.handleLogList)
	mux.HandleFunc("DELETE /api/logs", s.handleLogClear)

	mux.HandleFunc("GET /api/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetupSave)

	mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	mux.HandleFunc("POST /api/settings", s.handleSettingsSave)

	// ---------- 飞书深度整合：入站事件桥（公开端点，由 Encrypt Key / Token 保护） ----------
	mux.HandleFunc("POST /webhook/feishu", s.handleFeishuWebhook)
	mux.HandleFunc("POST /api/bridge/feishu/test", s.handleBridgeFeishuTest)
	mux.HandleFunc("GET /api/bridge/feishu/status", s.handleBridgeFeishuStatus)

	// ---------- 无人值守：自检、巡检历史、通知测试 ----------
	mux.HandleFunc("GET /api/selfcheck", s.handleSelfCheck)
	mux.HandleFunc("GET /api/health/history", s.handleHealthHistory)
	mux.HandleFunc("POST /api/notify/test", s.handleNotifyTest)

	mux.HandleFunc("POST /api/playground/chat", s.handlePlaygroundChat)
	mux.HandleFunc("POST /api/playground/image", s.handlePlaygroundImage)
	mux.HandleFunc("POST /api/playground/video", s.handlePlaygroundVideo)
	mux.HandleFunc("POST /api/playground/speech", s.handlePlaygroundSpeech)
	mux.HandleFunc("POST /api/playground/transcriptions", s.handlePlaygroundTranscription)

	mux.HandleFunc("POST /api/connectors/generate", s.handleConnectorGenerate)
	mux.HandleFunc("POST /api/connectors/try", s.handleConnectorTry)

	// ---------- 前端静态资源 ----------
	mux.Handle("/", s.staticHandler())
	return gzipMiddleware(logRequests(mux))
}

// ---------- 中间件 ----------

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// withKey 校验 Bearer 秘钥，并把秘钥信息传给业务处理函数。
func (s *Server) withKey(next func(http.ResponseWriter, *http.Request, store.APIKey)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "invalid_request_error", "缺少 API 秘钥，请在请求头带上 Authorization: Bearer sk-xxx")
			return
		}
		key, err := s.st.FindAPIKey(token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid_request_error", "API 秘钥无效")
			return
		}
		if !key.Enabled {
			writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥已被停用")
			return
		}
		if key.ExpiresAt != "" && expired(key.ExpiresAt) {
			writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥已过期")
			return
		}
		// 分钟级限流。
		if !s.limiter.allow(key.ID, key.RPM) {
			writeErr(w, http.StatusTooManyRequests, "rate_limit_error",
				fmt.Sprintf("超过每分钟请求上限（%d 次）", key.RPM))
			return
		}
		// 每日配额（次数 / token）。
		if key.DailyQuota > 0 || key.DailyTokens > 0 {
			calls, tokens, err := s.st.APIKeyDailyUsage(key.Name)
			if err == nil {
				if key.DailyQuota > 0 && calls >= int64(key.DailyQuota) {
					writeErr(w, http.StatusTooManyRequests, "quota_exceeded_error",
						fmt.Sprintf("今天的调用次数已达上限（%d 次）", key.DailyQuota))
					return
				}
				if key.DailyTokens > 0 && tokens >= int64(key.DailyTokens) {
					writeErr(w, http.StatusTooManyRequests, "quota_exceeded_error",
						fmt.Sprintf("今天的 token 用量已达上限（%d）", key.DailyTokens))
					return
				}
			}
		}
		s.st.TouchAPIKey(key.Key)
		next(w, r, key)
	}
}

func expired(ts string) bool {
	t := parseTime(ts)
	return !t.IsZero() && time.Now().After(t)
}

func parseTime(ts string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, strings.TrimSpace(ts)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		// 部分客户端用 api-key 头或 query 传参
		if v := r.Header.Get("x-api-key"); v != "" {
			return strings.TrimSpace(v)
		}
		return strings.TrimSpace(r.URL.Query().Get("key"))
	}
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(h)
}

// keyAllowed 判断秘钥是否被允许调用该模型。
func keyAllowed(k store.APIKey, model string) bool {
	if len(k.Models) == 0 {
		return true
	}
	for _, m := range k.Models {
		if m == model || m == "*" {
			return true
		}
	}
	return false
}

// ---------- 通用工具 ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, kind, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": kind, "code": status},
	})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeErr(w, status, "server_error", err.Error())
}

func decodeBody(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(out)
}

// decodeMap 把请求体读成宽松的 map，便于透传未知字段。
func decodeMap(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	var out map[string]any
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// normalizeList 把前端传来的字符串数组清洗一遍。
func normalizeList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
