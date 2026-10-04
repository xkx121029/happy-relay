package api

import (
	"encoding/json"
	"net/http"
)

// ---------- 批量操作 ----------
//
// 模型 / 渠道 / 秘钥三个列表页共用的批量模式：
// POST /api/{models,providers,keys}/batch  {"ids":[1,2,3], "action":"enable|disable|delete"}
//
// 启用/禁用走「读全量 → 改 Enabled → 存回」：三个 Save* 都是整体替换，
// 必须拿完整实体改一个字段再落库，避免请求体只传部分字段时把其余配置抹掉。

type batchReq struct {
	IDs    []int64 `json:"ids"`
	Action string  `json:"action"` // enable | disable | delete
}

func decodeBatchReq(w http.ResponseWriter, r *http.Request) (batchReq, bool) {
	var req batchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON")
		return req, false
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "ids 不能为空")
		return req, false
	}
	switch req.Action {
	case "enable", "disable", "delete":
	default:
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "action 仅支持 enable / disable / delete")
		return req, false
	}
	return req, true
}

// batchResult 汇总每个实体的成功/失败，failed 里放失败的 id，前端可提示具体数量。
type batchResult struct {
	OK       bool    `json:"ok"`
	Affected int     `json:"affected"`
	Failed   []int64 `json:"failed,omitempty"`
}

func runBatchIDs(ids []int64, fn func(id int64) error) batchResult {
	res := batchResult{OK: true}
	for _, id := range ids {
		if err := fn(id); err != nil {
			res.Failed = append(res.Failed, id)
			continue
		}
		res.Affected++
	}
	return res
}

func handleModelsBatch(s *Server, w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBatchReq(w, r)
	if !ok {
		return
	}
	res := runBatchIDs(req.IDs, func(id int64) error {
		if req.Action == "delete" {
			return s.st.DeleteModel(id)
		}
		m, err := s.st.GetModel(id)
		if err != nil {
			return err
		}
		m.Enabled = req.Action == "enable"
		_, err = s.st.SaveModel(m)
		return err
	})
	writeJSON(w, http.StatusOK, res)
}

func handleProvidersBatch(s *Server, w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBatchReq(w, r)
	if !ok {
		return
	}
	res := runBatchIDs(req.IDs, func(id int64) error {
		if req.Action == "delete" {
			return s.st.DeleteProvider(id)
		}
		p, err := s.st.GetProvider(id)
		if err != nil {
			return err
		}
		p.Enabled = req.Action == "enable"
		_, err = s.st.SaveProvider(p)
		return err
	})
	writeJSON(w, http.StatusOK, res)
}

func handleKeysBatch(s *Server, w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBatchReq(w, r)
	if !ok {
		return
	}
	res := runBatchIDs(req.IDs, func(id int64) error {
		if req.Action == "delete" {
			return s.st.DeleteAPIKey(id)
		}
		k, err := s.st.FindAPIKeyByID(id)
		if err != nil {
			return err
		}
		k.Enabled = req.Action == "enable"
		_, err = s.st.SaveAPIKey(k)
		return err
	})
	writeJSON(w, http.StatusOK, res)
}
