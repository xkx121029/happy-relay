package api

import (
	"net/http"
	"strings"

	"happy-relay/internal/store"
)

// handleModelFromPreset 用内置预设一键创建对外模型。
// 前端只需要给出预设 ID 与要绑定的渠道，其余参数都由预设提供。
func (s *Server) handleModelFromPreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PresetID   string `json:"preset_id"`
		ProviderID int64  `json:"provider_id"`
		Name       string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	preset, ok := store.FindModelPreset(req.PresetID)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "未知的模型预设："+req.PresetID)
		return
	}
	if req.ProviderID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请选择要绑定的渠道")
		return
	}
	// 渠道必须已配置密钥（即完成过配置）才允许挂预设模型——
	// 没配好的渠道创建出来的模型永远调不通，只会污染对外模型列表。
	prov, err := s.st.GetProvider(req.ProviderID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if len(prov.APIKeys) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"该渠道还没有配置 API 密钥：请在「内置模型 → 配置向导」完成配置并通过连通测试后再添加")
		return
	}
	// 允许覆盖对外模型名，其余参数沿用预设。
	if name := strings.TrimSpace(req.Name); name != "" {
		preset.Name = name
	}
	// 同名模型已存在时给出可读提示，而不是把 SQLite 的唯一约束错误透出去。
	if _, err := s.st.GetModelByName(preset.Name); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "name": preset.Name, "existed": true,
		})
		return
	}
	saved, err := s.st.SaveModel(preset.Model(req.ProviderID))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}
