package api

import (
	"sync"
	"time"

	"happy-relay/internal/store"
)

// usableCache 缓存「渠道可用性集合」，避免每个请求都查两次库
// （渠道列表 + 最近巡检结果）。TTL 30 秒：巡检/改配置后最多
// 半分钟同步，对可用性这种慢变量完全够用。
type usableCache struct {
	mu   sync.Mutex
	at   time.Time
	set  map[int64]bool
	list []store.Provider
}

const usableTTL = 30 * time.Second

func (s *Server) usableSnapshot() (map[int64]bool, []store.Provider, error) {
	s.us.mu.Lock()
	defer s.us.mu.Unlock()
	if s.us.set != nil && time.Since(s.us.at) < usableTTL {
		return s.us.set, s.us.list, nil
	}
	provs, err := s.st.ListProviders()
	if err != nil {
		return nil, nil, err
	}
	lastOK, err := s.st.LastHealthOKMap()
	if err != nil {
		return nil, nil, err
	}
	set := make(map[int64]bool, len(provs))
	for _, p := range provs {
		set[p.ID] = providerUsable(p, lastOK[p.ID])
	}
	s.us.set = set
	s.us.list = provs
	s.us.at = time.Now()
	return set, provs, nil
}

// providerUsable 判定渠道当前是否可能调通：
//   - 已停用 → 不可用；
//   - 配了密钥 → 可用；
//   - 没密钥 → 只有最近一次连通巡检通过才算可用（自建免密钥服务，
//     如 LM Studio / Ollama）。从未巡检通过且没密钥的内置渠道不可用。
func providerUsable(p store.Provider, lastHealthOK bool) bool {
	if !p.Enabled {
		return false
	}
	return len(p.APIKeys) > 0 || lastHealthOK
}

// mustListModels 读取全量模型列表；出错时返回空列表（由过滤逻辑兜底）。
func mustListModels(s *Server) []store.Model {
	list, err := s.st.ListModels()
	if err != nil {
		return nil
	}
	return list
}

// filterUsableModels 返回列表里「至少绑定了一个可用渠道」的模型。
func (s *Server) filterUsableModels(list []store.Model) []store.Model {
	set, _, err := s.usableSnapshot()
	if err != nil {
		return list
	}
	out := make([]store.Model, 0, len(list))
	for _, m := range list {
		for _, id := range m.AllProviderIDs() {
			if set[id] {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// modelUsableFlags 返回与列表等长的可用性标记（不过滤）。
func (s *Server) modelUsableFlags(list []store.Model) []bool {
	set, _, err := s.usableSnapshot()
	if err != nil {
		return nil
	}
	flags := make([]bool, len(list))
	for i, m := range list {
		for _, id := range m.AllProviderIDs() {
			if set[id] {
				flags[i] = true
				break
			}
		}
	}
	return flags
}
