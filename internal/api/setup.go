package api

import (
	"net/http"
	"strconv"
	"strings"

	"happy-relay/internal/store"
)

// SetupStatus 描述面板首次进入时的引导状态。
type SetupStatus struct {
	// Seeded 表示内置的免费渠道与模型已在部署时写入数据库。
	Seeded bool `json:"seeded"`
	// 内置渠道与其下的模型，用于在引导里展示。
	ProviderID   int64    `json:"provider_id"`
	ProviderName string   `json:"provider_name"`
	ProviderKind string   `json:"provider_kind"`
	Models       []string `json:"models"`
	// NeedsKey 为真表示内置渠道还没有 API Key，用户在引导里填好即可启用。
	NeedsKey bool `json:"needs_key"`
	// SignupURL 是该渠道申请 API Key 的地址。
	SignupURL string `json:"signup_url"`
	// AccessKeys 是已启用的访问秘钥数量，为 0 时引导顺手创建一个。
	AccessKeys int `json:"access_keys"`
}

// handleSetupStatus 返回引导状态，面板首次进入时调用。
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.setupStatus())
}

func (s *Server) setupStatus() SetupStatus {
	st := SetupStatus{Models: []string{}}
	// 播种标记现在存的是版本号，用数值比较避免旧值 "1" 与新版本号对不上。
	if v, _ := strconv.Atoi(s.st.GetSetting(store.SettingBuiltinSeeded, "0")); v < store.BuiltinSeedVersion {
		return st
	}
	st.Seeded = true

	id, _ := strconv.ParseInt(s.st.GetSetting(store.SettingBuiltinProviderID, "0"), 10, 64)
	if p, err := s.st.GetProvider(id); err == nil {
		st.ProviderID = p.ID
		st.ProviderName = p.Name
		st.ProviderKind = p.Kind
		st.SignupURL = store.SignupURL(p.Kind)
		st.NeedsKey = len(p.APIKeys) == 0

		if models, err := s.st.ListModels(); err == nil {
			for _, m := range models {
				for _, in := range m.AllInputs() {
					if in.ProviderID == p.ID {
						st.Models = append(st.Models, m.Name)
						break
					}
				}
			}
		}
	}

	if keys, err := s.st.ListAPIKeys(); err == nil {
		for _, k := range keys {
			if k.Enabled {
				st.AccessKeys++
			}
		}
	}
	return st
}

// handleSetupSave 处理首次引导的提交：写入内置渠道的 API Key，
// 并可顺带生成一个访问秘钥（明文只在本次响应里返回）。
func (s *Server) handleSetupSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKey          string `json:"api_key"`
		CreateAccessKey bool   `json:"create_access_key"`
		AccessKeyName   string `json:"access_key_name"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	resp := map[string]any{}

	if key := strings.TrimSpace(req.APIKey); key != "" {
		id, _ := strconv.ParseInt(s.st.GetSetting(store.SettingBuiltinProviderID, "0"), 10, 64)
		p, err := s.st.GetProvider(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "invalid_request_error",
				"内置渠道不存在（可能已被删除），可到「渠道」页手动添加")
			return
		}
		p.APIKeys = []string{key}
		p.Enabled = true
		saved, err := s.st.SaveProvider(p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		resp["saved_provider"] = saved.Name
	}

	if req.CreateAccessKey {
		name := strings.TrimSpace(req.AccessKeyName)
		if name == "" {
			name = "默认秘钥"
		}
		// Key 留空时由 store 生成。
		saved, err := s.st.SaveAPIKey(store.APIKey{Name: name, Enabled: true})
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		resp["access_key"] = saved.Key
	}

	resp["status"] = s.setupStatus()
	writeJSON(w, http.StatusOK, resp)
}
