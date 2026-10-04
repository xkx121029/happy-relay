package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// 管理面板锁：管理员密码由设置页配置（或环境变量兜底），此后所有管理类
// /api/* 请求都必须带上 X-Admin-Password 头。客户端输入的密码逐请求携带，
// 服务端只存哈希（sha256(salt|password)），库里不落明文。
//
// 没配置密码时一律放行：自用部署保持零摩擦，也保证首次引导能走通。
// 一旦设置了密码，面板就上锁——这是「裸路由对外、管理面板锁起来」的关键。
const (
	SettingAdminPassword = "admin_password"       // sha256(salt|password) 十六进制
	SettingAdminSalt     = "admin_password_salt" // 随机盐
	adminPasswordHeader  = "X-Admin-Password"
	adminLockHeader      = "X-Admin-Lock" // 401 时带回，前端据此切到锁屏
)

// adminHash 计算密码哈希。盐 + 分隔符 + 密码，避免短密码直接撞库。
func adminHash(salt, password string) string {
	sum := sha256.Sum256([]byte(salt + "|" + password))
	return hex.EncodeToString(sum[:])
}

// randomSalt 生成 16 字节随机盐（十六进制 32 字符）。
func randomSalt() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// adminGateEnabled 报告管理面板是否已上锁。
func (s *Server) adminGateEnabled() bool {
	if strings.TrimSpace(s.cfg.AdminPassword) != "" {
		return true
	}
	return strings.TrimSpace(s.st.GetSetting(SettingAdminPassword, "")) != ""
}

// checkAdminPassword 校验请求携带的管理员密码。未启用锁时一律通过。
func (s *Server) checkAdminPassword(password string) bool {
	// 环境变量优先：容器化部署的兜底换密码通道（忘记密码时用它救场）。
	if envPw := strings.TrimSpace(s.cfg.AdminPassword); envPw != "" {
		return subtle.ConstantTimeCompare([]byte(envPw), []byte(password)) == 1
	}
	hash := strings.TrimSpace(s.st.GetSetting(SettingAdminPassword, ""))
	if hash == "" {
		return true
	}
	salt := s.st.GetSetting(SettingAdminSalt, "")
	got := adminHash(salt, password)
	return subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1
}

// adminPublicPath 判断管理类接口里哪些路径不需要管理员密码。
// 用户门户用秘钥认证（/api/user/*），公告与外链是公开页要读的内容，
// /api/admin/* 是锁屏自身要用的状态与校验接口。
func adminPublicPath(method, path string) bool {
	switch {
	case strings.HasPrefix(path, "/api/admin/"):
		return true
	case strings.HasPrefix(path, "/api/user/"):
		return true
	case path == "/api/portal/links" && method == http.MethodGet:
		return true
	}
	return false
}

// withAdminGate 保护管理类 /api/*：已设密码且未携带正确密码时返回 401。
// /v1/*（客户端 OpenAI 兼容接口，走秘钥认证）与裸路由（面板静态页）不受影响。
func (s *Server) withAdminGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") || adminPublicPath(r.Method, path) || !s.adminGateEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		if s.checkAdminPassword(r.Header.Get(adminPasswordHeader)) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set(adminLockHeader, "1")
		writeErr(w, http.StatusUnauthorized, "admin_locked", "管理面板已锁定，请在面板中输入管理员密码")
	})
}

// ---------- 锁屏专用接口（无需密码） ----------

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	envPw := strings.TrimSpace(s.cfg.AdminPassword)
	writeJSON(w, http.StatusOK, map[string]any{
		"protected": s.adminGateEnabled(),
		"source":    adminGateSource(s.st.GetSetting(SettingAdminPassword, ""), envPw),
		"unlocked":  s.checkAdminPassword(r.Header.Get(adminPasswordHeader)),
	})
}

func adminGateSource(storedHash, envPw string) string {
	if envPw != "" {
		return "env"
	}
	if strings.TrimSpace(storedHash) != "" {
		return "settings"
	}
	return ""
}

// handleAdminUnlock 校验管理员密码：前端在勾选「记住本浏览器」前先验一次，
// 避免把错密码存进缓存导致后面所有请求都被拒。
func (s *Server) handleAdminUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if !s.adminGateEnabled() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "protected": false})
		return
	}
	if !s.checkAdminPassword(req.Password) {
		w.Header().Set(adminLockHeader, "1")
		writeErr(w, http.StatusUnauthorized, "admin_locked", "密码不对")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "protected": true})
}

// setAdminPassword 写入新密码（生成随机盐）。空密码等于关闭保护。
func (s *Server) setAdminPassword(password string) error {
	if strings.TrimSpace(password) == "" {
		if err := s.st.SetSetting(SettingAdminPassword, ""); err != nil {
			return err
		}
		return s.st.SetSetting(SettingAdminSalt, "")
	}
	salt, err := randomSalt()
	if err != nil {
		return err
	}
	if err := s.st.SetSetting(SettingAdminSalt, salt); err != nil {
		return err
	}
	return s.st.SetSetting(SettingAdminPassword, adminHash(salt, password))
}
