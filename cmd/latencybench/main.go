// 延迟对比基准：同一进程内「直连上游」vs「走引擎中转」，各跑 N 轮流式小请求。
// 目的：量化中转层（鉴权/限流/解析/转发）叠加在单次调用上的额外延迟。
// 用法：go run ./cmd/latencybench -db ./data-test/relay.db -model agnes -n 6
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

func main() {
	dbPath := flag.String("db", "./data-test/relay.db", "sqlite 路径")
	modelName := flag.String("model", "agnes", "对外模型名")
	n := flag.Int("n", 6, "每条链路轮数")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		panic(err)
	}
	defer st.Close()

	eng := relay.NewEngine(st, 3, 60, 600)
	m, ps, err := eng.ResolveAll(*modelName)
	if err != nil {
		panic(err)
	}
	p := ps[0]
	if len(p.APIKeys) == 0 || len(p.BaseURLs) == 0 {
		panic("渠道缺秘钥或入口地址")
	}
	upURL := strings.TrimRight(p.BaseURLs[0], "/")
	if !strings.HasSuffix(upURL, "/models") && !strings.Contains(upURL, "/v") {
		upURL += "/v1"
	}
	upURL += "/chat/completions"
	fmt.Printf("模型 %s → 渠道「%s」 %s (upstream=%s)\n\n", *modelName, p.Name, upURL, m.UpstreamName)

	client := &http.Client{Transport: relay.PooledTransport()}

	type res struct{ ttfb, total float64 }
	directTTFB, directTotal := []float64{}, []float64{}
	relayTTFB, relayTotal := []float64{}, []float64{}

	run := func(rounds int, viaRelay bool) {
		for i := 0; i < rounds; i++ {
			in := &relay.ChatInput{
				Model:    *modelName,
				Stream:   true,
				Messages: []relay.ChatMessage{{Role: "user", Content: "只回答两个字：好的"}},
				Options:  map[string]any{"max_tokens": 24, "stream": true},
				Raw:      map[string]any{},
			}
			start := time.Now()
			var ttfb float64
			var rerr error
			if viaRelay {
				_, _, rerr = eng.Chat(context.Background(), m, ps, in, func(d relay.StreamDelta) error {
					if ttfb == 0 {
						ttfb = float64(time.Since(start).Microseconds()) / 1000
					}
					return nil
				})
			} else {
				ttfb, rerr = directCall(client, upURL, p.APIKeys[0], m.UpstreamName)
			}
			total := float64(time.Since(start).Microseconds()) / 1000
			if rerr != nil {
				fmt.Printf("round %d (relay=%v) FAIL: %v\n", i, viaRelay, rerr)
				time.Sleep(2 * time.Second)
				continue
			}
			if viaRelay {
				relayTTFB = append(relayTTFB, ttfb)
				relayTotal = append(relayTotal, total)
			} else {
				directTTFB = append(directTTFB, ttfb)
				directTotal = append(directTotal, total)
			}
			time.Sleep(2 * time.Second)
		}
	}

	run(*n, false)
	run(*n, true)
	avg := func(a []float64) string {
		if len(a) == 0 {
			return "n/a"
		}
		var s float64
		for _, v := range a {
			s += v
		}
		return fmt.Sprintf("%.0fms", s/float64(len(a)))
	}
	fmt.Printf("直连上游  TTFB %s  total %s\n", avg(directTTFB), avg(directTotal))
	fmt.Printf("走引擎    TTFB %s  total %s\n", avg(relayTTFB), avg(relayTotal))
	if len(directTTFB) > 0 && len(relayTTFB) > 0 {
		d, r := avgf(directTTFB), avgf(relayTTFB)
		fmt.Printf("中转额外 TTFB 开销: +%.0fms\n", r-d)
	}
}

func avgf(a []float64) float64 {
	var s float64
	for _, v := range a {
		s += v
	}
	return s / float64(len(a))
}

// directCall 直连上游的流式调用，返回 TTFB(ms)。
func directCall(client *http.Client, url, key, upstreamModel string) (float64, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      upstreamModel,
		"messages":   []map[string]string{{"role": "user", "content": "只回答两个字：好的"}},
		"max_tokens": 24,
		"stream":     true,
	})
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var ttfb float64
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if ttfb == 0 && strings.HasPrefix(sc.Text(), "data:") {
			ttfb = float64(time.Since(start).Microseconds()) / 1000
		}
	}
	return ttfb, sc.Err()
}
