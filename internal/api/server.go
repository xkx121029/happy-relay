// Package api 提供两层 HTTP 接口：
//   - /v1/*  对外统一接口，需 Bearer 秘钥，供第三方客户端调用
//   - /api/* 面板管理接口，仅供自用网页面板调用
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"happy-relay/internal/config"
	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

type Server struct {
	cfg    *config.Config
	st     *store.Store
	eng    *relay.Engine
	static fs.FS
}

func New(cfg *config.Config, st *store.Store, eng *relay.Engine, static fs.FS) *Server {
	return &Server{cfg: cfg, st: st, eng: eng, static: static}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---------- 健康检查 ----------
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// ---------- 对外 v1 ----------
	mux.HandleFunc("GET /v1/models", s.withKey(s.handleModels))
	mux.HandleFunc("GET /v1/skills", s.withKey(s.handleSkillCatalog))
	mux.HandleFunc("POST /v1/chat/completions", s.withKey(s.handleChat))
	mux.HandleFunc("POST /v1/completions", s.withKey(s.handleLegacyCompletion))
	mux.HandleFunc("POST /v1/embeddings", s.withKey(s.handleEmbeddings))
	mux.HandleFunc("POST /v1/images/generations", s.withKey(s.handleImage))
	mux.HandleFunc("GET /v1/videos", s.withKey(s.handleVideoList))
	mux.HandleFunc("POST /v1/videos", s.withKey(s.handleVideoCreate))
	mux.HandleFunc("GET /v1/videos/{id}", s.withKey(s.handleVideoGet))
	mux.HandleFunc("DELETE /v1/videos/{id}", s.withKey(s.handleVideoDelete))
	mux.HandleFunc("GET /v1/videos/{id}/stream", s.withKey(s.handleVideoStream))
	// 兼容以 /generations 结尾的写法
	mux.HandleFunc("POST /v1/videos/generations", s.withKey(s.handleVideoCreate))
	mux.HandleFunc("GET /v1/videos/generations/{id}", s.withKey(s.handleVideoGet))

	// ---------- 面板管理 ----------
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/overview", s.handleOverview)

	mux.HandleFunc("GET /api/providers", s.handleProviderList)
	mux.HandleFunc("POST /api/providers", s.handleProviderSave)
	mux.HandleFunc("PUT /api/providers/{id}", s.handleProviderSave)
	mux.HandleFunc("DELETE /api/providers/{id}", s.handleProviderDelete)
	mux.HandleFunc("POST /api/providers/test", s.handleProviderTest)

	mux.HandleFunc("GET /api/models", s.handleModelList)
	mux.HandleFunc("POST /api/models", s.handleModelSave)
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

	mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	mux.HandleFunc("POST /api/settings", s.handleSettingsSave)

	mux.HandleFunc("POST /api/playground/chat", s.handlePlaygroundChat)
	mux.HandleFunc("POST /api/playground/image", s.handlePlaygroundImage)
	mux.HandleFunc("POST /api/playground/video", s.handlePlaygroundVideo)

	mux.HandleFunc("POST /api/connectors/generate", s.handleConnectorGenerate)
	mux.HandleFunc("POST /api/connectors/try", s.handleConnectorTry)

	// ---------- 前端静态资源 ----------
	mux.Handle("/", s.staticHandler())
	return logRequests(mux)
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
