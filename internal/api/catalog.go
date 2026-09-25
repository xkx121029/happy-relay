package api

import (
	"fmt"
	"net/http"

	"happy-relay/internal/store"
)

// ---------- 模型库 ----------
//
// 模型库保存的是「从渠道探测到或手工登记的上游模型」，仅作为配置候选，
// 不会直接对外暴露；只有把它们配进对外模型后，客户端才能调用。

func (s *Server) handleCatalogList(w http.ResponseWriter, r *http.Request) {
	providerID := int64(0)
	if v := r.URL.Query().Get("provider_id"); v != "" {
		fmt.Sscanf(v, "%d", &providerID)
	}
	list, err := s.st.ListCatalogModels(providerID, r.URL.Query().Get("capability"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCatalogSave(w http.ResponseWriter, r *http.Request) {
	var m store.CatalogModel
	if err := decodeBody(r, &m); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if id := pathID(r); id > 0 {
		m.ID = id
	}
	saved, err := s.st.SaveCatalogModel(m)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleCatalogDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteCatalogModel(pathID(r)); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCatalogImport 把渠道探测到的模型批量写入模型库（按渠道 + 模型名去重）。
func (s *Server) handleCatalogImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID int64                `json:"provider_id"`
		Models     []store.CatalogModel `json:"models"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if req.ProviderID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少 provider_id")
		return
	}
	if len(req.Models) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "没有要导入的模型")
		return
	}
	created, updated, err := s.st.ImportCatalogModels(req.ProviderID, req.Models)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "updated": updated})
}
