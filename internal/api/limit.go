package api

import (
	"sync"
	"time"
)

// modelLimiter 是按对外模型的 RPM（每分钟请求数）/ TPM（每分钟 token 数）滑动窗口限速器。
// 与按秘钥限速互补：秘钥限速管「谁能打多少」，模型限速管「某个上游模型被打多狠」。
type modelLimiter struct {
	mu   sync.Mutex
	reqs map[int64][]time.Time // modelID -> 请求时间戳
	toks map[int64][]tokenRec  // modelID -> token 消耗记录
}

type tokenRec struct {
	t time.Time
	n int
}

func newModelLimiter() *modelLimiter {
	return &modelLimiter{
		reqs: make(map[int64][]time.Time),
		toks: make(map[int64][]tokenRec),
	}
}

// allow 预检模型维度限速：返回 false 表示超限，应拒绝本次请求。
// estTokens 为本次预估消耗的 token（未知传 0），用于提前占住 TPM 窗口。
func (l *modelLimiter) allow(modelID int64, rpm, tpm, estTokens int) bool {
	if rpm <= 0 && tpm <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-time.Minute)

	if rpm > 0 {
		rs := l.reqs[modelID]
		kept := rs[:0]
		for _, t := range rs {
			if t.After(cut) {
				kept = append(kept, t)
			}
		}
		l.reqs[modelID] = kept
		if len(kept) >= rpm {
			return false
		}
	}

	if tpm > 0 {
		ts := l.toks[modelID]
		kept := ts[:0]
		sum := 0
		for _, r := range ts {
			if r.t.After(cut) {
				kept = append(kept, r)
				sum += r.n
			}
		}
		l.toks[modelID] = kept
		if sum+estTokens > tpm {
			return false
		}
	}
	return true
}

// record 在请求成功后登记实际消耗，供后续窗口统计。
func (l *modelLimiter) record(modelID int64, tokens int) {
	if tokens <= 0 {
		tokens = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.reqs[modelID] = append(l.reqs[modelID], now)
	l.toks[modelID] = append(l.toks[modelID], tokenRec{t: now, n: tokens})
}
