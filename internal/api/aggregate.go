package api

import (
	"fmt"
	"net/http"

	"happy-relay/internal/store"
)

// ---------- 多模态模型自动聚合 ----------
//
// 扫描模型库（catalog）里探测到的 chat 上游，找出「能读图」的视觉上游，自动生成两类聚合计划：
//  1. declare：已有对外模型引用了视觉上游，但没显式声明图片模态（按名字推断也认不出来）→ 补声明；
//  2. create ：视觉上游还没被任何对外模型引用 → 新建一个聚合模型，视觉上游做主，
//     并把同渠道的纯文本上游挂为故障转移——纯文本请求走便宜的文本上游，带图请求自动路由到视觉上游。
//
// 计划先生成、前端勾选确认后再应用，避免一口气改一堆配置。

// aggDeclareItem 给已有模型的上游补输入模态声明。
type aggDeclareItem struct {
	Key          string   `json:"key"`
	ModelID      int64    `json:"model_id"`
	ModelName    string   `json:"model_name"`
	ProviderID   int64    `json:"provider_id"`
	ProviderName string   `json:"provider_name"`
	UpstreamName string   `json:"upstream_name"`
	Modalities   []string `json:"modalities"` // 建议声明的输入模态
}

// aggCreateItem 新建一个聚合模型。
type aggCreateItem struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"` // 对外模型名（已做唯一化）
	ProviderID   int64    `json:"provider_id"`
	ProviderName string   `json:"provider_name"`
	UpstreamName string   `json:"upstream_name"` // 视觉主上游
	Modalities   []string `json:"modalities"`   // 主上游输入模态
	Failovers    []string `json:"failovers"`    // 同渠道纯文本上游（作纯文本请求的落点）
}

type aggPlan struct {
	Declares []aggDeclareItem `json:"declares"`
	Creates  []aggCreateItem  `json:"creates"`
}

type autoAggReq struct {
	Apply    bool     `json:"apply"`
	Declares []string `json:"declares"` // 要应用的 declare key
	Creates  []string `json:"creates"`  // 要应用的 create key
}

// catalogInputs 返回 catalog 条目的输入模态；探测没带模态时按名字推断兜底。
func catalogInputs(c store.CatalogModel) []string {
	if len(c.InputModalities) > 0 {
		return c.InputModalities
	}
	ins, _ := store.GuessModalities(c.ModelID, c.Capability)
	return ins
}

// inputCoveredImage 判断模型输入是否已覆盖图片模态：显式声明含 image，或未声明但名字推断为视觉。
func inputCoveredImage(in store.ModelInput) bool {
	if len(in.InputModalities) > 0 {
		for _, m := range in.InputModalities {
			if m == store.ModalityImage {
				return true
			}
		}
		return false
	}
	ins, _ := store.GuessModalities(in.UpstreamName, store.CapChat)
	for _, m := range ins {
		if m == store.ModalityImage {
			return true
		}
	}
	return false
}

// inputTextOnly 判断模型输入是否只会吃纯文本：显式声明只有 text，或未声明且名字推断只有 text。
func inputTextOnly(in store.ModelInput) bool {
	if len(in.InputModalities) > 0 {
		return len(in.InputModalities) == 1 && in.InputModalities[0] == store.ModalityText
	}
	ins, _ := store.GuessModalities(in.UpstreamName, store.CapChat)
	return len(ins) == 1 && ins[0] == store.ModalityText
}

// upstreamKey 是「渠道 + 上游模型名」的唯一标识。
func upstreamKey(providerID int64, name string) string {
	return fmt.Sprintf("%d::%s", providerID, name)
}

func (s *Server) handleAutoAggregate(w http.ResponseWriter, r *http.Request) {
	var req autoAggReq
	_ = decodeBody(r, &req) // body 可省略，空 body = 只出计划

	plan, err := s.autoAggregatePlan()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if !req.Apply {
		writeJSON(w, http.StatusOK, plan)
		return
	}
	// 前端按 declares/creates 分组传 key，agent 传平铺列表——合并后按 key 前缀归属。
	keys := append(append([]string{}, req.Declares...), req.Creates...)
	applied, err := s.autoAggregateApply(plan, keys)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": applied})
}

// autoAggregatePlan 扫描模型库生成多模态聚合计划（纯计算，不落库）。
func (s *Server) autoAggregatePlan() (aggPlan, error) {
	plan := aggPlan{Declares: []aggDeclareItem{}, Creates: []aggCreateItem{}}

	provEnabled := map[int64]bool{}
	provName := map[int64]string{}
	if provs, err := s.st.ListProviders(); err == nil {
		for _, p := range provs {
			provEnabled[p.ID] = p.Enabled
			provName[p.ID] = p.Name
		}
	}

	cat, err := s.st.ListCatalogModels(0, store.CapChat)
	if err != nil {
		return plan, err
	}

	models, err := s.st.ListModels()
	if err != nil {
		return plan, err
	}

	// 统计现有模型对上游的引用情况。
	usedAny := map[string]bool{}
	for _, m := range models {
		if m.Capability != store.CapChat {
			continue
		}
		for _, in := range m.AllInputs() {
			usedAny[upstreamKey(in.ProviderID, in.UpstreamName)] = true
		}
	}

	// 渠道维度的模型库索引。
	catByKey := map[string]store.CatalogModel{}
	textOnlyByProvider := map[int64][]store.CatalogModel{}
	visionByProvider := map[int64][]store.CatalogModel{}
	for _, c := range cat {
		if !c.Enabled || !provEnabled[c.ProviderID] {
			continue
		}
		ins := catalogInputs(c)
		catByKey[upstreamKey(c.ProviderID, c.ModelID)] = c
		vision := false
		for _, m := range ins {
			if m == store.ModalityImage {
				vision = true
				break
			}
		}
		if vision {
			visionByProvider[c.ProviderID] = append(visionByProvider[c.ProviderID], c)
		} else if len(ins) == 1 && ins[0] == store.ModalityText {
			textOnlyByProvider[c.ProviderID] = append(textOnlyByProvider[c.ProviderID], c)
		}
	}

	usedName := map[string]bool{}
	for _, m := range models {
		usedName[m.Name] = true
	}

	// 1) 补声明：模型引用了视觉上游，但输入没声明图片模态且名字也推断不出来。
	di := 0
	for _, m := range models {
		if m.Capability != store.CapChat {
			continue
		}
		for _, in := range m.AllInputs() {
			if inputCoveredImage(in) {
				continue
			}
			c, ok := catByKey[upstreamKey(in.ProviderID, in.UpstreamName)]
			if !ok {
				continue
			}
			ins := catalogInputs(c)
			hasImage := false
			for _, mm := range ins {
				if mm == store.ModalityImage {
					hasImage = true
					break
				}
			}
			if !hasImage {
				continue
			}
			plan.Declares = append(plan.Declares, aggDeclareItem{
				Key:          fmt.Sprintf("d%d", di),
				ModelID:      m.ID,
				ModelName:    m.Name,
				ProviderID:   in.ProviderID,
				ProviderName: provName[in.ProviderID],
				UpstreamName: in.UpstreamName,
				Modalities:   ins,
			})
			di++
		}
	}

	// 2) 新建聚合模型：视觉上游还没被任何模型引用。
	ci := 0
	for _, c := range cat {
		ins := catalogInputs(c)
		hasImage := false
		for _, mm := range ins {
			if mm == store.ModalityImage {
				hasImage = true
				break
			}
		}
		if !hasImage || !c.Enabled || !provEnabled[c.ProviderID] {
			continue
		}
		if usedAny[upstreamKey(c.ProviderID, c.ModelID)] {
			continue
		}
		// 命名：直接用上游模型名；重名则追加 -vision / -vision-2。
		name := c.ModelID
		for suffix := ""; usedName[name]; suffix = nextVisionSuffix(suffix) {
			name = c.ModelID + suffix
		}
		usedName[name] = true

		// 纯文本故障转移：优先挑已被现有模型引用过的（配置被验证过），最多 2 个。
		var used, unused []string
		for _, t := range textOnlyByProvider[c.ProviderID] {
			if usedAny[upstreamKey(c.ProviderID, t.ModelID)] {
				used = append(used, t.ModelID)
			} else {
				unused = append(unused, t.ModelID)
			}
		}
		failovers := append(used, unused...)
		if len(failovers) > 2 {
			failovers = failovers[:2]
		}

		plan.Creates = append(plan.Creates, aggCreateItem{
			Key:          fmt.Sprintf("c%d", ci),
			Name:         name,
			ProviderID:   c.ProviderID,
			ProviderName: c.ProviderName,
			UpstreamName: c.ModelID,
			Modalities:   ins,
			Failovers:    failovers,
		})
		ci++
	}

	return plan, nil
}

// autoAggregateApply 按 key 集合应用聚合计划（补声明 + 新建模型），返回应用条数。
func (s *Server) autoAggregateApply(plan aggPlan, keys []string) (int, error) {
	sel := map[string]bool{}
	for _, k := range keys {
		sel[k] = true
	}
	if len(sel) == 0 {
		return 0, fmt.Errorf("请至少勾选一项再应用")
	}

	applied := 0
	for _, d := range plan.Declares {
		if !sel[d.Key] {
			continue
		}
		m, err := s.st.GetModel(d.ModelID)
		if err != nil {
			continue
		}
		inputs := m.Inputs
		if len(inputs) == 0 {
			// 旧数据没有 inputs，先展开再改。
			inputs = m.AllInputs()
		}
		changed := false
		for i := range inputs {
			if inputs[i].ProviderID == d.ProviderID && inputs[i].UpstreamName == d.UpstreamName {
				inputs[i].InputModalities = d.Modalities
				changed = true
			}
		}
		if !changed {
			continue
		}
		m.Inputs = inputs
		if _, err := s.st.SaveModel(m); err == nil {
			applied++
		}
	}

	for _, c := range plan.Creates {
		if !sel[c.Key] {
			continue
		}
		inputs := []store.ModelInput{{
			ProviderID:      c.ProviderID,
			UpstreamName:    c.UpstreamName,
			InputModalities: c.Modalities,
		}}
		for _, f := range c.Failovers {
			inputs = append(inputs, store.ModelInput{
				ProviderID:      c.ProviderID,
				UpstreamName:    f,
				InputModalities: []string{store.ModalityText},
			})
		}
		m := store.Model{
			Name:         c.Name,
			UpstreamName: c.UpstreamName,
			Capability:   store.CapChat,
			Enabled:      true,
			Inputs:       inputs,
			Remark:       "自动聚合：视觉主上游 + 纯文本故障转移",
		}
		m.Config.RouteStrategy = "failover"
		m.ProviderID = c.ProviderID
		if _, err := s.st.SaveModel(m); err == nil {
			applied++
		}
	}

	return applied, nil
}

// nextVisionSuffix 生成 -vision、-vision-2、-vision-3… 的后缀序列。
func nextVisionSuffix(cur string) string {
	if cur == "" {
		return "-vision"
	}
	// 解析 -vision-N
	var n int
	fmt.Sscanf(cur, "-vision-%d", &n)
	if n == 0 {
		return "-vision-2"
	}
	return fmt.Sprintf("-vision-%d", n+1)
}
