package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// responseCache 是进程内的非流式响应缓存，按「模型名 + 规范化请求体」做键。
// 命中后直接回包，不再回源，省 token 也降延迟。流式与调试台专用请求不缓存。
type responseCache struct {
	mu   sync.RWMutex
	item map[string]cacheEntry
	cap  int
}

type cacheEntry struct {
	data   []byte
	ctype  string
	expire int64 // 纳秒时间戳，0 表示永不过期（由上层 TTL 控制）
}

func newResponseCache() *responseCache {
	return &responseCache{item: make(map[string]cacheEntry), cap: 2000}
}

func (c *responseCache) get(key string) ([]byte, string, bool) {
	c.mu.RLock()
	e, ok := c.item[key]
	c.mu.RUnlock()
	if !ok {
		return nil, "", false
	}
	if e.expire > 0 && time.Now().UnixNano() > e.expire {
		c.mu.Lock()
		delete(c.item, key)
		c.mu.Unlock()
		return nil, "", false
	}
	return e.data, e.ctype, true
}

func (c *responseCache) set(key string, data []byte, ctype string, ttlSec int) {
	if ttlSec <= 0 {
		return
	}
	c.mu.Lock()
	if len(c.item) >= c.cap {
		// 简单失效策略：移除一半，缓存量不大，可接受。
		removed := 0
		for k := range c.item {
			delete(c.item, k)
			removed++
			if removed >= c.cap/2 {
				break
			}
		}
	}
	var exp int64
	if ttlSec > 0 {
		exp = time.Now().Add(time.Duration(ttlSec) * time.Second).UnixNano()
	}
	c.item[key] = cacheEntry{data: data, ctype: ctype, expire: exp}
	c.mu.Unlock()
}

// cacheKey 生成缓存键：模型名 + 规范化请求体。
// encoding/json 对 map 键排序，因此同一请求的键稳定；排除流式与调试台专用字段。
func cacheKey(model string, body map[string]any) string {
	norm := make(map[string]any, len(body))
	skip := map[string]bool{"stream": true, "provider_id": true, "upstream_name": true, "api_key": true, "key": true}
	for k, v := range body {
		if skip[k] {
			continue
		}
		norm[k] = v
	}
	b, err := json.Marshal(norm)
	if err != nil {
		b = []byte(model)
	}
	sum := sha256.Sum256(append([]byte(model+"|"), b...))
	return hex.EncodeToString(sum[:])
}
