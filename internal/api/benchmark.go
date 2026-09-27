package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// ---------- 模型智力测评 ----------

// 主模型（设置里的系统级 AI）当考官：给每个文本模型出固定题目，
// 按参考答案与指令遵循程度打 0-100 分，并把分数与档位写回模型配置。
// auto 路由在同档位内会优先把任务派给高分模型，让「谁更聪明」由实测决定，
// 而不是靠模型名里的 mini/pro 字样猜。

// benchSystemPrompt 是考官的系统提示：只输出 JSON 评分。
const benchSystemPrompt = `你是严格的大模型能力评审。给你一道题目、参考答案要点、程序校验结果与被测模型的回答，
请按正确性、完整性与指令遵循程度打 0-100 的整数分：
- 完全正确且符合格式要求：80-100
- 大体正确但有遗漏、小错或格式瑕疵：50-79
- 明显错误、答非所问或未按格式：0-49
程序校验结果为「精确匹配」时分数不应低于 85；为「不匹配」时不应高于 60。
只输出一个 JSON 对象：{"score": <0-100 的整数>, "reason": "<一句话理由>"}`

// benchQuestion 是一道固定测评题。Strict 非空时先做程序化精确校验，
// 结果作为考官评分的硬约束，避免考官模型自己算错题。
type benchQuestion struct {
	Q    string // 题目（含作答格式要求）
	Ref  string // 参考答案要点
	Hit  string // 精确答案（做归一化后的包含匹配），空表示只能靠考官判
	Desc string // 展示用的题目名
}

var benchQuestions = []benchQuestion{
	{
		Desc: "数学应用题",
		Q:    "小明买了 3 支笔，每支 2.5 元，他付了 20 元。请问他应该被找回多少钱？只回答数字，不要单位，不要解释。",
		Ref:  "12.5",
		Hit:  "12.5",
	},
	{
		Desc: "逻辑推理题",
		Q:    "鸡兔同笼：笼子里共有 10 个头、28 只脚。鸡和兔各有几只？用一行回答，格式：鸡=数字,兔=数字",
		Ref:  "鸡=6,兔=4（鸡 6 只 12 只脚，兔 4 只 16 只脚，共 10 头 28 脚）",
		Hit:  "鸡=6",
	},
	{
		Desc: "指令遵循题",
		Q:    `请严格输出一个 JSON 对象，不要输出任何其他内容（包括代码块标记）。要求：键 name 的值为 "测试"，键 items 的值为恰好 3 个元素的数组 [1,2,3]。`,
		Ref:  `{"name":"测试","items":[1,2,3]}`,
		Hit:  "",
	},
}

// benchItem 是单道题的测评结果。
type benchItem struct {
	Question string `json:"question"`
	Score    int    `json:"score"`
	Reason   string `json:"reason"`
	StrictOK bool   `json:"strict_ok"`
}

// handleModelBenchmark 对单个模型做智力测评：
// 逐题让被测模型作答 → 主模型评分 → 汇总均分 → 写回 tier / intelligence。
func (s *Server) handleModelBenchmark(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	name := strings.TrimSpace(req.Model)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请指定要测评的模型名")
		return
	}
	if name == relay.AutoModelName {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "auto 是路由规则不是模型，不需要测评")
		return
	}
	systemModel := s.st.GetSetting(SettingSystemModel, "")
	if systemModel == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"尚未指定主模型，请先在「设置 → 通用 → 系统级 AI 模型」里选择一个文本模型")
		return
	}
	if name == systemModel {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "主模型是考官，不能测评自己")
		return
	}
	m, err := s.st.GetModelByName(name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if m.Capability != store.CapChat {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "智力测评只支持文本对话模型")
		return
	}

	items := make([]benchItem, 0, len(benchQuestions))
	total := 0
	for _, q := range benchQuestions {
		item, err := s.benchOne(r.Context(), systemModel, m.Name, q)
		if err != nil {
			// 单题失败即中止：多半是渠道挂了或没配密钥，继续考也没有意义。
			writeErr(w, http.StatusBadGateway, "upstream_error",
				"测评中断（"+q.Desc+"）："+err.Error())
			return
		}
		items = append(items, item)
		total += item.Score
	}

	score := total / len(benchQuestions)
	tier := tierByScore(score)

	// 只在原配置上更新测评产物，其余字段原样保留（SaveModel 是整体覆盖）。
	m.Config.Tier = tier
	m.Config.Intelligence = score
	m.Config.BenchmarkedAt = store.NowString()
	if _, err := s.st.SaveModel(m); err != nil {
		writeStoreErr(w, err)
		return
	}

	s.logUsage(store.UsageLog{
		Capability: "benchmark", Model: name, APIKeyName: panelKey.Name,
		Status: 200, LatencyMS: 0,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"model": name, "score": score, "tier": tier, "items": items,
	})
}

// benchOne 考一道题：被测模型作答 + 程序校验 + 主模型评分。
func (s *Server) benchOne(ctx context.Context, systemModel, modelName string, q benchQuestion) (benchItem, error) {
	// 作答：给被测模型 2 分钟，慢思考模型也能完成。
	answerCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	m, ps, err := s.eng.ResolveAll(modelName)
	if err != nil {
		return benchItem{}, err
	}
	in := &relay.ChatInput{
		Model: modelName,
		Messages: []relay.ChatMessage{
			{Role: "user", Content: q.Q},
		},
		Options: map[string]any{"temperature": 0},
		Raw:     map[string]any{},
	}
	out, _, err := s.eng.Chat(answerCtx, m, ps, in, nil)
	if err != nil {
		return benchItem{}, err
	}
	answer := strings.TrimSpace(out.Content)

	// 程序化精确校验：归一化后做包含匹配（容忍单位、句号等噪音）。
	strictOK := false
	if q.Hit != "" {
		strictOK = strings.Contains(normalizeBenchText(answer), normalizeBenchText(q.Hit))
	}

	// 主模型评分。
	judgePrompt := "题目：" + q.Q +
		"\n参考答案要点：" + q.Ref +
		"\n程序校验结果：" + map[bool]string{true: "精确匹配", false: "不匹配或本题无程序校验"}[strictOK] +
		"\n被测模型的回答：" + answer
	judgeCtx, cancelJudge := context.WithTimeout(ctx, 60*time.Second)
	defer cancelJudge()
	jOut, _, err := s.callSystemAI(judgeCtx, systemModel, benchSystemPrompt, judgePrompt)
	if err != nil {
		return benchItem{}, err
	}
	score, reason := parseBenchVerdict(jOut.Content)
	// 精确匹配保底 85 分，防止考官手松。
	if strictOK && score < 85 {
		score = 85
		reason = "精确匹配，保底 85 分；" + reason
	}
	return benchItem{Question: q.Desc, Score: score, Reason: reason, StrictOK: strictOK}, nil
}

// normalizeBenchText 把回答压成只含字母数字与小数点的形态，供精确比对。
func normalizeBenchText(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseBenchVerdict 从考官回复里取分数与理由；解析失败按 0 分处理。
func parseBenchVerdict(reply string) (int, string) {
	text := extractJSONObject(reply)
	if text == "" {
		return 0, "考官未返回可识别的评分"
	}
	var v struct {
		Score  int    `json:"score"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return 0, "考官评分解析失败"
	}
	if v.Score < 0 {
		v.Score = 0
	}
	if v.Score > 100 {
		v.Score = 100
	}
	return v.Score, v.Reason
}

// tierByScore 把智力分映射到档位：
// 75 分以上算强力（复杂推理），45-74 标准（日常任务），45 以下轻量（短问答）。
func tierByScore(score int) string {
	switch {
	case score >= 75:
		return store.TierPro
	case score >= 45:
		return store.TierStandard
	default:
		return store.TierLight
	}
}
