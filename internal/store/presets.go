package store

import "strconv"

// ---------- 内置免费模型预设 ----------

// SettingBuiltinSeeded 记录内置免费渠道与模型是否已写入数据库。
// 用户删掉内置模型后不会又被塞回来，因为标记与渠道 / 模型本身的存亡无关。
const SettingBuiltinSeeded = "builtin_seeded"

// BuiltinSeedVersion 是内置预设的版本号。新增平台后把它 +1，
// 已部署的实例会在下次启动时补种新增的内置渠道；
// 已存在的同名渠道不会被覆盖，用户删掉的也不会被复活。
//
// v4：只保留「永久免费（无需充值）」的平台；播种不再直接创建模型——
// 模型只在渠道配置完成且连通测试通过后，由面板「配置向导」写入。
const BuiltinSeedVersion = 4

// SettingBuiltinProviderID 记录内置渠道的 ID，供面板首次进入时定位引导目标。
const SettingBuiltinProviderID = "builtin_provider_id"

// ModelPreset 是一个开箱即用的模型预设：
// 只要选好渠道，其余参数（上游模型名、能力、模态、上下文、视频模板）都会自动填好。
type ModelPreset struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderKind string `json:"provider_kind"` // 需要绑定哪种渠道类型
	ProviderName string `json:"provider_name"` // 建议的渠道名
	UpstreamName string `json:"upstream_name"`
	Capability   string `json:"capability"`
	// ContextLength 是官方公布的上下文窗口，会写入模型配置。
	ContextLength    int      `json:"context_length,omitempty"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Free             bool     `json:"free"`
	Description      string   `json:"description"`
	// DefaultParams 是建议写入模型「默认参数」的内容。
	DefaultParams map[string]any `json:"default_params,omitempty"`
	// Video 为真时表示该预设需要一并写入内置的视频提交 / 查询模板。
	Video bool `json:"video,omitempty"`

	// ---------- 渠道落点（同一平台的所有预设共用） ----------

	// ProviderBaseURL 是该平台的内置入口地址。国内主流大模型平台
	// （硅基流动、魔搭、百炼、千帆、火山方舟、讯飞星火 …）协议都是 OpenAI 兼容，
	// 差异只在 base_url 与模型名，因此统一用 KindOpenAI 承载，
	// 由预设把入口地址带进去，避免为每个平台新增一种渠道类型。
	ProviderBaseURL string `json:"provider_base_url,omitempty"`
	// SignupURL 是申请该平台 API Key 的地址，供面板直接给出跳转链接。
	SignupURL string `json:"signup_url,omitempty"`
	// ProviderNote 是该平台的开通前置条件提示（实名认证、绑阿里云账号等）。
	ProviderNote string `json:"provider_note,omitempty"`
	// NoVersionPath 为真时，该平台接口直接挂在根路径下（如 GitHub Models），
	// 不再按 OpenAI 兼容协议自动补 /v1，否则会被拼成 404。
	NoVersionPath bool `json:"no_version_path,omitempty"`
	// RealnameRequired 为真表示开通该平台需要实名认证（大陆平台普遍要求）。
	// 面板据此把平台分成「无需实名」与「需实名」两类，供用户按需选择。
	RealnameRequired bool `json:"realname_required,omitempty"`
}

// Model 把预设转成绑定到指定渠道的对外模型。
// 面板的一键导入与首次启动的内置播种共用这份转换，避免两处参数走偏。
func (p ModelPreset) Model(providerID int64) Model {
	cfg := ModelConfig{ContextLength: p.ContextLength}
	if len(p.DefaultParams) > 0 {
		cfg.Defaults = p.DefaultParams
	}
	if p.Video {
		submit, query := ZhipuVideoSpecs()
		cfg.Video, cfg.VideoQuery = &submit, &query
	}
	return Model{
		Name:         p.Name,
		UpstreamName: p.UpstreamName,
		Capability:   p.Capability,
		Inputs: []ModelInput{{
			ProviderID:       providerID,
			UpstreamName:     p.UpstreamName,
			InputModalities:  p.InputModalities,
			OutputModalities: p.OutputModalities,
		}},
		Config:  cfg,
		Enabled: true,
		Remark:  p.Description,
	}
}

// SignupURL 返回各渠道类型对应的 API Key 申请地址，供面板引导用户去获取。
// 内置平台渠道走 KindOpenAI，申请地址随预设携带（见 ModelPreset.SignupURL）。
func SignupURL(kind string) string {
	switch kind {
	case KindZhipu:
		return "https://open.bigmodel.cn/usercenter/apikeys"
	case KindOpenAI:
		return "https://platform.openai.com/api-keys"
	case KindGemini:
		return "https://aistudio.google.com/app/apikey"
	case KindAnthropic:
		return "https://console.anthropic.com/settings/keys"
	}
	return ""
}

// joinModalities 按顺序合并多组模态并去重，避免在字面量里嵌套 append。
func joinModalities(groups ...[]string) []string {
	out := []string{}
	for _, g := range groups {
		out = append(out, g...)
	}
	return CleanModalities(out)
}

// 内置平台的入口地址。集中定义，避免同一平台的多个预设各写一遍字符串写错。
const (
	baseSiliconFlow = "https://api.siliconflow.cn/v1"
	baseModelScope  = "https://api-inference.modelscope.cn/v1"
	baseQianfan     = "https://qianfan.baidubce.com/v2"
	baseSpark       = "https://spark-api-open.xf-yun.com/v1"
	baseOpenRouter  = "https://openrouter.ai/api/v1"
	baseNvidiaNIM   = "https://integrate.api.nvidia.com/v1"
	baseGroq        = "https://api.groq.com/openai/v1"
	baseHunyuan     = "https://api.hunyuan.cloud.tencent.com/v1"
)

// ModelPresets 返回内置的永久免费模型预设（无需充值，仅有账号 / 实名 / 限速门槛）。
// 按「无需实名」与「需实名」两类打标，供面板筛选展示。
//
// 说明：免费政策与模型 ID 由各平台随时调整，这里的记录核对于 2026-09。
// 开通后建议在「模型库」页点一次「探测」，以平台实际返回的模型列表为准。
func ModelPresets() []ModelPreset {
	text := []string{ModalityText}
	image := []string{ModalityImage}
	video := []string{ModalityVideo}
	file := []string{ModalityFile}

	// 视觉模型与视觉推理模型都能读图 / 视频 / 文档，只有 4v-flash 仅支持图片。
	visionFull := joinModalities(text, image, video, file)
	visionImage := joinModalities(text, image)

	zhipu := []ModelPreset{
		{
			ID: "zhipu-glm-4.7-flash", Name: "GLM-4.7-Flash",
			ProviderKind: KindZhipu, ProviderName: "智谱 GLM",
			UpstreamName: "glm-4.7-flash", Capability: CapChat,
			ContextLength: 200000, Free: true,
			InputModalities: text, OutputModalities: text,
			Description: "免费文本模型，200K 上下文，默认开启深度思考",
			// 默认开启思考模式；不想要就删掉这条默认参数。
			DefaultParams: map[string]any{"thinking": map[string]any{"type": "enabled"}},
			SignupURL:     SignupURL(KindZhipu),
			ProviderNote:  "智谱开放平台，需实名认证；GLM-Flash 系列为官方永久免费模型。",
		},
		{
			ID: "zhipu-glm-4.6v-flash", Name: "GLM-4.6V-Flash",
			ProviderKind: KindZhipu, ProviderName: "智谱 GLM",
			UpstreamName: "glm-4.6v-flash", Capability: CapChat,
			ContextLength: 128000, Free: true,
			InputModalities: visionFull, OutputModalities: text,
			Description:  "免费视觉模型，可读图 / 视频 / 文档，128K 上下文",
			SignupURL:    SignupURL(KindZhipu),
			ProviderNote: "智谱开放平台，需实名认证；GLM-Flash 系列为官方永久免费模型。",
		},
		{
			ID: "zhipu-glm-4v-flash", Name: "GLM-4V-Flash",
			ProviderKind: KindZhipu, ProviderName: "智谱 GLM",
			UpstreamName: "glm-4v-flash", Capability: CapChat,
			Free:             true,
			InputModalities:  visionImage,
			OutputModalities: text,
			Description:      "免费图像理解模型，适合图表问答与图片描述",
			SignupURL:        SignupURL(KindZhipu),
			ProviderNote:     "智谱开放平台，需实名认证；GLM-Flash 系列为官方永久免费模型。",
		},
		{
			ID: "zhipu-glm-4.1v-thinking-flash", Name: "GLM-4.1V-Thinking-Flash",
			ProviderKind: KindZhipu, ProviderName: "智谱 GLM",
			UpstreamName: "glm-4.1v-thinking-flash", Capability: CapChat,
			Free:             true,
			InputModalities:  visionFull,
			OutputModalities: text,
			Description:      "免费视觉推理模型，内置深度思考，擅长图表与 GUI 任务",
			SignupURL:        SignupURL(KindZhipu),
			ProviderNote:     "智谱开放平台，需实名认证；GLM-Flash 系列为官方永久免费模型。",
		},
		{
			ID: "zhipu-cogview-3-flash", Name: "CogView-3-Flash",
			ProviderKind: KindZhipu, ProviderName: "智谱 GLM",
			UpstreamName: "cogview-3-flash", Capability: CapImage,
			Free:             true,
			InputModalities:  text,
			OutputModalities: image,
			Description:      "免费文生图模型，数秒出图",
			// 该模型的 size 是固定枚举，给一个稳妥的默认值。
			DefaultParams: map[string]any{"size": "1024x1024"},
			SignupURL:     SignupURL(KindZhipu),
			ProviderNote:  "智谱开放平台，需实名认证；GLM-Flash 系列为官方永久免费模型。",
		},
		{
			ID: "zhipu-cogvideox-flash", Name: "CogVideoX-Flash",
			ProviderKind: KindZhipu, ProviderName: "智谱 GLM",
			UpstreamName: "cogvideox-flash", Capability: CapVideo,
			Free:             true,
			InputModalities:  joinModalities(text, image),
			OutputModalities: video,
			Description:      "免费视频生成模型，支持文生视频与图生视频",
			Video:            true,
			SignupURL:        SignupURL(KindZhipu),
			ProviderNote:     "智谱开放平台，需实名认证；GLM-Flash 系列为官方永久免费模型。",
		},
	}

	// 硅基流动：9B 参数以下的小模型官方承诺永久免费，另有注册赠送额度。
	// 模型 ID 带社团前缀，必须原样透传。
	silicon := []ModelPreset{
		{
			ID: "silicon-qwen3-8b", Name: "Qwen3-8B（硅基流动）",
			ProviderKind: KindOpenAI, ProviderName: "硅基流动 SiliconFlow",
			ProviderBaseURL: baseSiliconFlow, SignupURL: "https://cloud.siliconflow.cn/account/ak",
			UpstreamName: "Qwen/Qwen3-8B", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "永久免费的 8B 小模型，日常问答与文案够用，限速 1000 RPM",
			ProviderNote: "需实名认证；9B 以下小模型永久免费，注册另赠初始额度。",
		},
		{
			ID: "silicon-deepseek-r1-7b", Name: "DeepSeek-R1-Distill-Qwen-7B（硅基流动）",
			ProviderKind: KindOpenAI, ProviderName: "硅基流动 SiliconFlow",
			ProviderBaseURL: baseSiliconFlow, SignupURL: "https://cloud.siliconflow.cn/account/ak",
			UpstreamName: "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "永久免费，把 R1 的推理能力压进 7B，适合数学与代码",
			ProviderNote: "需实名认证；9B 以下小模型永久免费，注册另赠初始额度。",
		},
		{
			ID: "silicon-glm-4-9b", Name: "GLM-4-9B-Chat（硅基流动）",
			ProviderKind: KindOpenAI, ProviderName: "硅基流动 SiliconFlow",
			ProviderBaseURL: baseSiliconFlow, SignupURL: "https://cloud.siliconflow.cn/account/ak",
			UpstreamName: "THUDM/glm-4-9b-chat", Capability: CapChat,
			ContextLength: 32768, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "永久免费，中文对话与长文本理解表现稳定",
			ProviderNote: "需实名认证；9B 以下小模型永久免费，注册另赠初始额度。",
		},
		{
			ID: "silicon-glm-4.1v-9b", Name: "GLM-4.1V-9B-Thinking（硅基流动）",
			ProviderKind: KindOpenAI, ProviderName: "硅基流动 SiliconFlow",
			ProviderBaseURL: baseSiliconFlow, SignupURL: "https://cloud.siliconflow.cn/account/ak",
			UpstreamName: "THUDM/GLM-4.1V-9B-Thinking", Capability: CapChat,
			ContextLength: 66560, Free: true,
			InputModalities: visionImage, OutputModalities: text,
			Description:  "永久免费的视觉推理小模型，可读图并输出思考过程",
			ProviderNote: "需实名认证；9B 以下小模型永久免费，注册另赠初始额度。",
		},
		{
			ID: "silicon-deepseek-ocr", Name: "DeepSeek-OCR（硅基流动）",
			ProviderKind: KindOpenAI, ProviderName: "硅基流动 SiliconFlow",
			ProviderBaseURL: baseSiliconFlow, SignupURL: "https://cloud.siliconflow.cn/account/ak",
			UpstreamName: "deepseek-ai/DeepSeek-OCR", Capability: CapChat,
			Free:             true,
			InputModalities:  visionImage,
			OutputModalities: text,
			Description:      "永久免费的文档识别模型，OCR 与表格提取专用",
			ProviderNote:     "需实名认证；9B 以下小模型永久免费，注册另赠初始额度。",
		},
	}

	// 魔搭 ModelScope（阿里）：每天 2000 次免费调用，单模型上限 500 次。
	modelscope := []ModelPreset{
		{
			ID: "modelscope-qwen3.5-35b", Name: "Qwen3.5-35B-A3B（魔搭）",
			ProviderKind: KindOpenAI, ProviderName: "魔搭 ModelScope",
			ProviderBaseURL: baseModelScope, SignupURL: "https://modelscope.cn/my/myaccesstoken",
			UpstreamName: "Qwen/Qwen3.5-35B-A3B", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: visionImage, OutputModalities: text,
			Description:  "每日 2000 次免费调用，MoE 稀疏激活，文本 + 视觉",
			ProviderNote: "必须先在魔搭绑定阿里云账号，否则调用直接报错。",
		},
		{
			ID: "modelscope-qwen3.5-27b", Name: "Qwen3.5-27B（魔搭）",
			ProviderKind: KindOpenAI, ProviderName: "魔搭 ModelScope",
			ProviderBaseURL: baseModelScope, SignupURL: "https://modelscope.cn/my/myaccesstoken",
			UpstreamName: "Qwen/Qwen3.5-27B", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "每日 2000 次免费调用，纯文本稠密模型",
			ProviderNote: "必须先在魔搭绑定阿里云账号，否则调用直接报错。",
		},
		{
			ID: "modelscope-qwen-image", Name: "Qwen-Image（魔搭）",
			ProviderKind: KindOpenAI, ProviderName: "魔搭 ModelScope",
			ProviderBaseURL: baseModelScope, SignupURL: "https://modelscope.cn/my/myaccesstoken",
			UpstreamName: "Qwen/Qwen-Image", Capability: CapImage,
			Free:             true,
			InputModalities:  text,
			OutputModalities: image,
			Description:      "每日免费额度内可用的文生图模型",
			ProviderNote:     "必须先在魔搭绑定阿里云账号；生成类模型有单独配额。",
		},
	}

	// 阿里云百炼 DashScope 与火山方舟（豆包）此前收录过，但它们的免费额度
	// 是「试用赠送 / 每日限量」，并非永久免费模型，v4 起不再收录。

	// 百度千帆：ERNIE-Speed / ERNIE-3.5-8K 永久免费不限量。
	qianfan := []ModelPreset{
		{
			ID: "qianfan-ernie-speed-8k", Name: "ERNIE-Speed-8K（千帆）",
			ProviderKind: KindOpenAI, ProviderName: "百度千帆",
			ProviderBaseURL: baseQianfan, SignupURL: "https://console.bce.baidu.com/iam/#/iam/apikey/list",
			UpstreamName: "ernie-speed-8k", Capability: CapChat,
			ContextLength: 8192, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "官方永久免费不限量，QPS 较高，适合国内合规场景",
			ProviderNote: "需实名认证；ERNIE-Speed / ERNIE-3.5-8K 永久免费。",
		},
		{
			ID: "qianfan-ernie-3.5-8k", Name: "ERNIE-3.5-8K（千帆）",
			ProviderKind: KindOpenAI, ProviderName: "百度千帆",
			ProviderBaseURL: baseQianfan, SignupURL: "https://console.bce.baidu.com/iam/#/iam/apikey/list",
			UpstreamName: "ernie-3.5-8k", Capability: CapChat,
			ContextLength: 8192, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "官方永久免费不限量，中文理解扎实",
			ProviderNote: "需实名认证；ERNIE-Speed / ERNIE-3.5-8K 永久免费。",
		},
	}

	// 讯飞星火：spark-lite 永久免费，Token 不限量，QPS=2。
	spark := []ModelPreset{
		{
			ID: "spark-lite", Name: "Spark-Lite（讯飞星火）",
			ProviderKind: KindOpenAI, ProviderName: "讯飞星火",
			ProviderBaseURL: baseSpark, SignupURL: "https://console.xfyun.cn/services/bm4",
			UpstreamName: "lite", Capability: CapChat,
			ContextLength: 8192, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "永久免费、Token 不限量，仅受 QPS=2 限制",
			ProviderNote: "需实名认证；HTTP 端点的密钥格式为「APIKey:APISecret」拼接，文档见 xf-yun.com。",
		},
	}

	// OpenRouter：名称带 :free 后缀的模型免费，每天 200 次请求。
	openrouter := []ModelPreset{
		{
			ID: "openrouter-deepseek-r1-free", Name: "DeepSeek-R1（OpenRouter 免费）",
			ProviderKind: KindOpenAI, ProviderName: "OpenRouter",
			ProviderBaseURL: baseOpenRouter, SignupURL: "https://openrouter.ai/settings/keys",
			UpstreamName: "deepseek/deepseek-r1:free", Capability: CapChat,
			ContextLength: 163840, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "免费池里的强推理模型，每天 200 次请求",
			ProviderNote: "免费模型 ID 必须带 :free 后缀；国内访问需自备网络条件，部分模型会 403。",
		},
		{
			ID: "openrouter-llama-3.3-70b-free", Name: "Llama-3.3-70B（OpenRouter 免费）",
			ProviderKind: KindOpenAI, ProviderName: "OpenRouter",
			ProviderBaseURL: baseOpenRouter, SignupURL: "https://openrouter.ai/settings/keys",
			UpstreamName: "meta-llama/llama-3.3-70b-instruct:free", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "免费池里的通用大模型，中英双语均衡",
			ProviderNote: "免费模型 ID 必须带 :free 后缀；国内访问需自备网络条件，部分模型会 403。",
		},
		{
			ID: "openrouter-qwen3-235b-free", Name: "Qwen3-235B-A22B（OpenRouter 免费）",
			ProviderKind: KindOpenAI, ProviderName: "OpenRouter",
			ProviderBaseURL: baseOpenRouter, SignupURL: "https://openrouter.ai/settings/keys",
			UpstreamName: "qwen/qwen3-235b-a22b:free", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "免费池里的超大 MoE，中文表现好",
			ProviderNote: "免费模型 ID 必须带 :free 后缀；国内访问需自备网络条件，部分模型会 403。",
		},
		{
			ID: "openrouter-gemini-flash-free", Name: "Gemini-2.0-Flash（OpenRouter 免费）",
			ProviderKind: KindOpenAI, ProviderName: "OpenRouter",
			ProviderBaseURL: baseOpenRouter, SignupURL: "https://openrouter.ai/settings/keys",
			UpstreamName: "google/gemini-2.0-flash-exp:free", Capability: CapChat,
			ContextLength: 1048576, Free: true,
			InputModalities: visionImage, OutputModalities: text,
			Description:  "免费池里的长上下文视觉模型，1M 窗口",
			ProviderNote: "免费模型 ID 必须带 :free 后缀；国内访问需自备网络条件，部分模型会 403。",
		},
	}

	// NVIDIA NIM：开发者计划免费，约 40 RPM，无每日 token 上限。模型 ID 带组织前缀。
	nvidia := []ModelPreset{
		{
			ID: "nvidia-deepseek-r1", Name: "DeepSeek-R1（NVIDIA NIM）",
			ProviderKind: KindOpenAI, ProviderName: "NVIDIA NIM",
			ProviderBaseURL: baseNvidiaNIM, SignupURL: "https://build.nvidia.com/settings/api-keys",
			UpstreamName: "deepseek-ai/deepseek-r1", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "免费但限速约 40 RPM，不设每日 token 上限",
			ProviderNote: "需注册 NVIDIA 开发者计划并做手机号验证；仅限原型与评估用途。",
		},
		{
			ID: "nvidia-nemotron-super-49b", Name: "Llama-3.3-Nemotron-Super-49B（NVIDIA NIM）",
			ProviderKind: KindOpenAI, ProviderName: "NVIDIA NIM",
			ProviderBaseURL: baseNvidiaNIM, SignupURL: "https://build.nvidia.com/settings/api-keys",
			UpstreamName: "nvidia/llama-3.3-nemotron-super-49b-v1.5", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "面向 Agent 场景优化的开源模型，TensorRT 加速",
			ProviderNote: "需注册 NVIDIA 开发者计划并做手机号验证；仅限原型与评估用途。",
		},
		{
			ID: "nvidia-llama-3.2-11b-vision", Name: "Llama-3.2-11B-Vision（NVIDIA NIM）",
			ProviderKind: KindOpenAI, ProviderName: "NVIDIA NIM",
			ProviderBaseURL: baseNvidiaNIM, SignupURL: "https://build.nvidia.com/settings/api-keys",
			UpstreamName: "meta/llama-3.2-11b-vision-instruct", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: visionImage, OutputModalities: text,
			Description:  "免费视觉模型，适合图片理解与图文问答",
			ProviderNote: "需注册 NVIDIA 开发者计划并做手机号验证；仅限原型与评估用途。",
		},
	}

	// Groq：免费层每日额度较大，推理速度极快。
	groq := []ModelPreset{
		{
			ID: "groq-llama-3.3-70b", Name: "Llama-3.3-70B-Versatile（Groq）",
			ProviderKind: KindOpenAI, ProviderName: "Groq",
			ProviderBaseURL: baseGroq, SignupURL: "https://console.groq.com/keys",
			UpstreamName: "llama-3.3-70b-versatile", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "免费层可用，LPU 推理速度极快",
			ProviderNote: "免费层有每日请求与 token 上限；国内访问需自备网络条件。",
		},
		{
			ID: "groq-gpt-oss-120b", Name: "GPT-OSS-120B（Groq）",
			ProviderKind: KindOpenAI, ProviderName: "Groq",
			ProviderBaseURL: baseGroq, SignupURL: "https://console.groq.com/keys",
			UpstreamName: "openai/gpt-oss-120b", Capability: CapChat,
			ContextLength: 131072, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "免费层可用的大参数开源模型",
			ProviderNote: "免费层有每日请求与 token 上限；国内访问需自备网络条件。",
		},
	}

	// DeepSeek / Moonshot Kimi / MiniMax / 阶跃星辰此前收录过，但它们都是
	// 「注册送额度、持续使用需充值」的按量计费平台，并非永久免费，v4 起不再收录。

	// 腾讯混元：hunyuan-lite 官方永久免费。turbos 只有每月额度，v4 起不再收录。
	hunyuan := []ModelPreset{
		{
			ID: "hunyuan-lite", Name: "Hunyuan-Lite（腾讯混元）",
			ProviderKind: KindOpenAI, ProviderName: "腾讯混元",
			ProviderBaseURL: baseHunyuan, SignupURL: "https://console.cloud.tencent.com/hunyuan/api-key",
			UpstreamName: "hunyuan-lite", Capability: CapChat,
			ContextLength: 262144, Free: true,
			InputModalities: text, OutputModalities: text,
			Description:  "官方永久免费，256K 上下文，适合国内合规场景兜底",
			ProviderNote: "hunyuan-lite 永久免费，需腾讯云账号（实名认证）。",
		},
	}

	out := make([]ModelPreset, 0, 32)
	out = append(out, zhipu...)
	out = append(out, silicon...)
	out = append(out, modelscope...)
	out = append(out, qianfan...)
	out = append(out, spark...)
	out = append(out, openrouter...)
	out = append(out, nvidia...)
	out = append(out, groq...)
	out = append(out, hunyuan...)

	// realnameByPlatform 标记各平台是否需要实名认证。
	// 大陆平台普遍要求实名；海外平台（Groq / OpenRouter / NVIDIA）
	// 只要注册账号，NVIDIA 另需手机号验证但不是实名认证。
	realnameByPlatform := map[string]bool{
		"智谱 GLM":           true,
		"硅基流动 SiliconFlow": true,
		"魔搭 ModelScope":    true,
		"百度千帆":             true,
		"讯飞星火":             true,
		"腾讯混元":             true,
		"OpenRouter":       false,
		"NVIDIA NIM":       false,
		"Groq":             false,
	}
	for i := range out {
		out[i].RealnameRequired = realnameByPlatform[out[i].ProviderName]
	}
	return out
}

// FindModelPreset 按 ID 查找预设。
func FindModelPreset(id string) (ModelPreset, bool) {
	for _, p := range ModelPresets() {
		if p.ID == id {
			return p, true
		}
	}
	return ModelPreset{}, false
}

// ZhipuVideoSpecs 返回智谱视频生成的提交与查询模板。
// 创建走 /videos/generations 拿到 id，查询走 /async-result/{id}，
// 视频地址在 video_result[0].url；查询响应不含状态字段，
// 因此由「是否已产出视频地址」判定完成（见 relay.PollVideo）。
func ZhipuVideoSpecs() (TemplateSpec, TemplateSpec) {
	submit := TemplateSpec{
		Method: "POST", Path: "/videos/generations",
		// 空参数不下发：智谱对无关字段较敏感。
		OmitEmpty: true,
		Body: map[string]any{
			"model":      "{{upstream_model}}",
			"prompt":     "{{prompt}}",
			"image_url":  "{{image_url}}",
			"size":       "{{size}}",
			"duration":   "{{duration}}",
			"fps":        30,
			"with_audio": false,
		},
		Response: ResponseMap{
			TaskID: "id", Status: "task_status", VideoURL: "video_result[0].url",
			StatusMap: map[string]string{
				"processing": "running",
				"success":    "succeeded",
				"fail":       "failed",
			},
		},
	}
	query := TemplateSpec{
		Method: "GET", Path: "/async-result/{{task_id}}",
		Response: ResponseMap{
			TaskID:   "id",
			VideoURL: "video_result[0].url",
			Error:    "error.message",
		},
	}
	return submit, query
}

// ---------- 首次启动播种 ----------

// seedBuiltinPresets 首次启动时把内置的免费渠道写入数据库，
// 让用户部署完就能在面板里看到它们。模型不随播种创建：只有渠道配置完成
// 且连通测试通过后，才由面板「配置向导」把模型纳入对外列表；
// 没配好的平台只出现在「内置模型」目录里。
//
// 内置渠道默认不带 API Key，由面板首次进入时引导填写。
// 用「内置预设版本号」而不是布尔标记控制：版本升级时补种新增的平台，
// 已经存在或已被用户删除的渠道都不会被改动。
func (s *Store) seedBuiltinPresets() error {
	if v, _ := strconv.Atoi(s.GetSetting(SettingBuiltinSeeded, "0")); v >= BuiltinSeedVersion {
		return nil
	}
	presets := ModelPresets()
	if len(presets) == 0 {
		return nil
	}

	// 同一「渠道类型 + 渠道名」的预设共用一个渠道。
	type groupKey struct{ kind, name string }
	seen := map[groupKey]bool{}
	order := []groupKey{}
	// 记录每个渠道的内置入口地址，播种时一并写入，省得用户再手填 base_url。
	baseOf := map[groupKey]string{}
	noteOf := map[groupKey]string{}
	noVersionPathOf := map[groupKey]bool{}
	for _, p := range presets {
		k := groupKey{p.ProviderKind, p.ProviderName}
		if !seen[k] {
			seen[k] = true
			order = append(order, k)
			baseOf[k] = p.ProviderBaseURL
			noteOf[k] = p.ProviderNote
			noVersionPathOf[k] = p.NoVersionPath
		}
	}

	existing, err := s.ListProviders()
	if err != nil {
		return err
	}
	byName := map[string]Provider{}
	for _, p := range existing {
		byName[p.Name] = p
	}

	firstID := int64(0)
	for _, k := range order {
		if p, ok := byName[k.name]; ok {
			if firstID == 0 {
				firstID = p.ID
			}
			continue
		}
		remark := "内置免费模型渠道，填入 API Key 后即可使用"
		if note := noteOf[k]; note != "" {
			remark = note
		}
		p := Provider{
			Name:          k.name,
			Kind:          k.kind,
			Enabled:       true,
			Remark:        remark,
			NoVersionPath: noVersionPathOf[k],
		}
		// 平台自带入口地址时直接写入；否则留给协议层的官方默认地址。
		if base := baseOf[k]; base != "" {
			p.BaseURLs = []string{base}
		}
		saved, err := s.SaveProvider(p)
		if err != nil {
			return err
		}
		if firstID == 0 {
			firstID = saved.ID
		}
	}

	models, err := s.ListModels()
	if err != nil {
		return err
	}

	// v4 起播种不再直接创建对外模型：模型只由「配置向导」在渠道配置完成
	// 且连通测试通过后写入，没配好的平台只留在「内置模型」目录里。
	// 这里顺带清理 v3 及更早版本播种、但所属内置渠道至今没有密钥的模型——
	// 它们从来就调不通，留在对外模型列表里只会误导。
	presetName := map[string]bool{}
	for _, p := range presets {
		presetName[p.Name] = true
	}
	builtinProvider := map[int64]bool{}
	for _, p := range existing {
		if _, ok := byName[p.Name]; ok && len(p.APIKeys) == 0 {
			builtinProvider[p.ID] = true
		}
	}
	for _, m := range models {
		if !presetName[m.Name] {
			continue
		}
		pids := m.ProviderIDs
		if len(pids) == 0 && m.ProviderID > 0 {
			pids = []int64{m.ProviderID}
		}
		if len(pids) == 0 {
			continue
		}
		orphan := true
		for _, pid := range pids {
			if !builtinProvider[pid] {
				orphan = false
				break
			}
		}
		if orphan {
			_ = s.DeleteModel(m.ID)
		}
	}

	// 只在首次播种时记录「引导目标渠道」，补种时不要把引导指向新平台。
	if firstID > 0 && s.GetSetting(SettingBuiltinProviderID, "") == "" {
		if err := s.SetSetting(SettingBuiltinProviderID, strconv.FormatInt(firstID, 10)); err != nil {
			return err
		}
	}
	return s.SetSetting(SettingBuiltinSeeded, strconv.Itoa(BuiltinSeedVersion))
}
