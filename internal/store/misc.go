package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// ---------- 站内技能 ----------

type SkillParam struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // text | textarea | number | select
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// Skill 是站内预置或用户保存的动作模板。
type Skill struct {
	ID          int64        `json:"id"`
	Slug        string       `json:"slug"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Category    string       `json:"category"`
	Icon        string       `json:"icon"`
	Capability  string       `json:"capability"` // chat | image | video
	Prompt      string       `json:"prompt"`
	Params      []SkillParam `json:"params"`
	Builtin     bool         `json:"builtin"`
	Enabled     bool         `json:"enabled"`
	SortOrder   int          `json:"sort_order"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

const skillSelect = `SELECT id,slug,name,description,category,icon,capability,prompt,params,
	builtin,enabled,sort_order,created_at,updated_at FROM skills`

func (s *Store) ListSkills() ([]Skill, error) {
	rows, err := s.db.Query(skillSelect + ` ORDER BY builtin DESC, sort_order ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		sk, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

func (s *Store) GetSkill(id int64) (Skill, error) {
	sk, err := scanSkill(s.db.QueryRow(skillSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Skill{}, ErrNotFound
	}
	return sk, err
}

func scanSkill(sc scanner) (Skill, error) {
	var sk Skill
	var params string
	var builtin, enabled int
	if err := sc.Scan(&sk.ID, &sk.Slug, &sk.Name, &sk.Description, &sk.Category, &sk.Icon,
		&sk.Capability, &sk.Prompt, &params, &builtin, &enabled, &sk.SortOrder,
		&sk.CreatedAt, &sk.UpdatedAt); err != nil {
		return Skill{}, err
	}
	sk.Params = unmarshal(params, []SkillParam{})
	sk.Builtin = builtin == 1
	sk.Enabled = enabled == 1
	return sk, nil
}

func (s *Store) SaveSkill(sk Skill) (Skill, error) {
	sk.Name = strings.TrimSpace(sk.Name)
	if sk.Name == "" {
		return sk, errors.New("技能名称不能为空")
	}
	if sk.Slug == "" {
		sk.Slug = slugify(sk.Name)
	}
	if sk.Capability == "" {
		sk.Capability = CapChat
	}
	if sk.Params == nil {
		sk.Params = []SkillParam{}
	}
	ts := now()
	if sk.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO skills
			(slug,name,description,category,icon,capability,prompt,params,builtin,enabled,sort_order,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,0,?,?,?,?)`,
			sk.Slug, sk.Name, sk.Description, sk.Category, sk.Icon, sk.Capability,
			sk.Prompt, marshal(sk.Params), boolInt(sk.Enabled), sk.SortOrder, ts, ts)
		if err != nil {
			return sk, err
		}
		sk.ID, _ = res.LastInsertId()
		return s.GetSkill(sk.ID)
	}
	if _, err := s.db.Exec(`UPDATE skills SET name=?,description=?,category=?,icon=?,
		capability=?,prompt=?,params=?,enabled=?,sort_order=?,updated_at=? WHERE id=?`,
		sk.Name, sk.Description, sk.Category, sk.Icon, sk.Capability, sk.Prompt,
		marshal(sk.Params), boolInt(sk.Enabled), sk.SortOrder, ts, sk.ID); err != nil {
		return sk, err
	}
	return s.GetSkill(sk.ID)
}

// DeleteSkill 只允许删除非预置技能。
func (s *Store) DeleteSkill(id int64) error {
	sk, err := s.GetSkill(id)
	if err != nil {
		return err
	}
	if sk.Builtin {
		return errors.New("预置技能不可删除")
	}
	_, err = s.db.Exec(`DELETE FROM skills WHERE id=?`, id)
	return err
}

// ---------- 视频任务 ----------

const (
	TaskQueued    = "queued"
	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
)

type VideoTask struct {
	ID             int64           `json:"id"`
	TaskID         string          `json:"task_id"`
	Model          string          `json:"model"`
	ProviderID     int64           `json:"provider_id"`
	ProviderName   string          `json:"provider_name,omitempty"`
	UpstreamTaskID string          `json:"upstream_task_id"`
	UpstreamURL    string          `json:"upstream_url"`
	Status         string          `json:"status"`
	Progress       int             `json:"progress"`
	Request        json.RawMessage `json:"request"`
	Result         json.RawMessage `json:"result"`
	Error          string          `json:"error"`
	APIKeyID       int64           `json:"api_key_id"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

const taskSelect = `SELECT t.id,t.task_id,t.model,t.provider_id,COALESCE(p.name,''),
	t.upstream_task_id,t.upstream_url,t.status,t.progress,t.request,t.result,t.error,
	t.api_key_id,t.created_at,t.updated_at FROM video_tasks t
	LEFT JOIN providers p ON p.id=t.provider_id`

func (s *Store) CreateVideoTask(t VideoTask) (VideoTask, error) {
	ts := now()
	if t.Status == "" {
		t.Status = TaskQueued
	}
	if len(t.Request) == 0 {
		t.Request = json.RawMessage("{}")
	}
	if len(t.Result) == 0 {
		t.Result = json.RawMessage("{}")
	}
	res, err := s.db.Exec(`INSERT INTO video_tasks
		(task_id,model,provider_id,upstream_task_id,upstream_url,status,progress,request,result,error,api_key_id,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.TaskID, t.Model, t.ProviderID, t.UpstreamTaskID, t.UpstreamURL, t.Status,
		t.Progress, string(t.Request), string(t.Result), t.Error, t.APIKeyID, ts, ts)
	if err != nil {
		return t, err
	}
	t.ID, _ = res.LastInsertId()
	t.CreatedAt, t.UpdatedAt = ts, ts
	return t, nil
}

func (s *Store) UpdateVideoTask(t VideoTask) error {
	_, err := s.db.Exec(`UPDATE video_tasks SET upstream_task_id=?,upstream_url=?,status=?,
		progress=?,result=?,error=?,updated_at=? WHERE id=?`,
		t.UpstreamTaskID, t.UpstreamURL, t.Status, t.Progress,
		string(orEmpty(t.Result)), t.Error, now(), t.ID)
	return err
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func (s *Store) GetVideoTaskByTaskID(taskID string) (VideoTask, error) {
	return s.scanTask(s.db.QueryRow(taskSelect+` WHERE t.task_id=?`, taskID))
}

func (s *Store) ListVideoTasks(limit int) ([]VideoTask, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(taskSelect+` ORDER BY t.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VideoTask{}
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListPendingVideoTasks 返回仍需轮询的上游任务。
func (s *Store) ListPendingVideoTasks() ([]VideoTask, error) {
	rows, err := s.db.Query(taskSelect + ` WHERE t.status IN ('queued','running')
		AND t.upstream_task_id<>'' ORDER BY t.id ASC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VideoTask{}
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) scanTask(sc scanner) (VideoTask, error) {
	t, err := scanTaskRow(sc)
	if errors.Is(err, sql.ErrNoRows) {
		return VideoTask{}, ErrNotFound
	}
	return t, err
}

func scanTaskRow(sc scanner) (VideoTask, error) {
	var t VideoTask
	var req, res string
	if err := sc.Scan(&t.ID, &t.TaskID, &t.Model, &t.ProviderID, &t.ProviderName,
		&t.UpstreamTaskID, &t.UpstreamURL, &t.Status, &t.Progress, &req, &res,
		&t.Error, &t.APIKeyID, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return VideoTask{}, err
	}
	t.Request = json.RawMessage(req)
	t.Result = json.RawMessage(res)
	return t, nil
}

// ---------- 调用日志 ----------

type UsageLog struct {
	ID               int64  `json:"id"`
	CreatedAt        string `json:"created_at"`
	Capability       string `json:"capability"`
	Model            string `json:"model"`
	ProviderName     string `json:"provider_name"`
	APIKeyName       string `json:"api_key_name"`
	Status           int    `json:"status"`
	Stream           bool   `json:"stream"`
	LatencyMS        int64  `json:"latency_ms"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Error            string `json:"error"`
	RequestBody      string `json:"request_body,omitempty"`
	ResponsePreview  string `json:"response_preview,omitempty"`
}

func (s *Store) AddUsageLog(l UsageLog) {
	if l.CreatedAt == "" {
		l.CreatedAt = now()
	}
	_, _ = s.db.Exec(`INSERT INTO usage_logs
		(created_at,capability,model,provider_name,api_key_name,status,stream,latency_ms,
		 prompt_tokens,completion_tokens,error,request_body,response_preview)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.CreatedAt, l.Capability, l.Model, l.ProviderName, l.APIKeyName, l.Status,
		boolInt(l.Stream), l.LatencyMS, l.PromptTokens, l.CompletionTokens, l.Error,
		l.RequestBody, l.ResponsePreview)
}

func (s *Store) ListUsageLogs(limit int, capability, keyword string) ([]UsageLog, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id,created_at,capability,model,provider_name,api_key_name,status,stream,
		latency_ms,prompt_tokens,completion_tokens,error,request_body,response_preview
		FROM usage_logs WHERE 1=1`
	args := []any{}
	if capability != "" {
		q += ` AND capability=?`
		args = append(args, capability)
	}
	if keyword != "" {
		q += ` AND (model LIKE ? OR provider_name LIKE ? OR error LIKE ? OR api_key_name LIKE ?)`
		like := "%" + keyword + "%"
		args = append(args, like, like, like, like)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageLog{}
	for rows.Next() {
		var l UsageLog
		var stream int
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.Capability, &l.Model, &l.ProviderName,
			&l.APIKeyName, &l.Status, &stream, &l.LatencyMS, &l.PromptTokens,
			&l.CompletionTokens, &l.Error, &l.RequestBody, &l.ResponsePreview); err != nil {
			return nil, err
		}
		l.Stream = stream == 1
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) ClearUsageLogs() error {
	_, err := s.db.Exec(`DELETE FROM usage_logs`)
	return err
}

// TrimUsageLogs 将日志裁剪到 keep 条以内。
func (s *Store) TrimUsageLogs(keep int) {
	if keep <= 0 {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM usage_logs WHERE id NOT IN
		(SELECT id FROM usage_logs ORDER BY id DESC LIMIT ?)`, keep)
}

// Stats 汇总面板概览数据。
type Stats struct {
	Providers    int     `json:"providers"`
	Models       int     `json:"models"`
	Keys         int     `json:"keys"`
	Skills       int     `json:"skills"`
	TodayCalls   int64   `json:"today_calls"`
	TotalCalls   int64   `json:"total_calls"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	PromptTokens int64   `json:"prompt_tokens"`
	CompTokens   int64   `json:"completion_tokens"`
	FailedCalls  int64   `json:"failed_calls"`
}

func (s *Store) Stats() (Stats, error) {
	var st Stats
	_ = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM providers),(SELECT COUNT(*) FROM models),
		(SELECT COUNT(*) FROM api_keys),(SELECT COUNT(*) FROM skills)`).
		Scan(&st.Providers, &st.Models, &st.Keys, &st.Skills)
	today := strings.Split(now(), "T")[0] + "T00:00:00"
	_ = s.db.QueryRow(`SELECT COUNT(*),COALESCE(AVG(latency_ms),0),
		COALESCE(SUM(prompt_tokens),0),COALESCE(SUM(completion_tokens),0),
		COALESCE(SUM(CASE WHEN status>=400 OR error<>'' THEN 1 ELSE 0 END),0)
		FROM usage_logs`).Scan(&st.TotalCalls, &st.AvgLatencyMS, &st.PromptTokens,
		&st.CompTokens, &st.FailedCalls)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE created_at>=?`, today).Scan(&st.TodayCalls)
	if st.TotalCalls > 0 {
		st.SuccessRate = float64(st.TotalCalls-st.FailedCalls) / float64(st.TotalCalls) * 100
	} else {
		st.SuccessRate = 100
	}
	return st, nil
}

// DailyCalls 返回最近 n 天每天的调用次数，用于概览趋势图。
func (s *Store) DailyCalls(days int) ([]map[string]any, error) {
	if days <= 0 {
		days = 7
	}
	rows, err := s.db.Query(`SELECT substr(created_at,1,10) AS d,
		COUNT(*),COALESCE(SUM(CASE WHEN status>=400 OR error<>'' THEN 1 ELSE 0 END),0)
		FROM usage_logs GROUP BY d ORDER BY d DESC LIMIT ?`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var d string
		var total, failed int64
		if err := rows.Scan(&d, &total, &failed); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"date": d, "total": total, "failed": failed})
	}
	return out, nil
}

// ---------- 设置 ----------

func (s *Store) GetSetting(key, def string) string {
	var v string
	err := s.db.QueryRow(`SELECT v FROM settings WHERE k=?`, key).Scan(&v)
	if err != nil {
		return def
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(k,v) VALUES(?,?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
	return err
}

func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT k,v FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// BuiltinSkills 返回软件预置的站内技能。
// 依约定：技能模板由软件预设，AI 不得自行新增；用户确认后可另存为自定义技能。
func BuiltinSkills() []Skill {
	return []Skill{
		{
			Slug: "chat-default", Name: "通用对话", Category: "文本", Icon: "chat",
			Description: "直接与所选模型对话，适合日常问答与快速验证链路。",
			Capability:  CapChat, Prompt: "{{input}}", SortOrder: 10,
			Params: []SkillParam{{
				Key: "input", Label: "提问内容", Type: "textarea",
				Placeholder: "输入你想问模型的内容…",
			}},
		},
		{
			Slug: "polish", Name: "文案润色", Category: "文本", Icon: "wand",
			Description: "把口语化或生硬的表达改写成通顺、专业的书面中文。",
			Capability:  CapChat, SortOrder: 20,
			Prompt: "你是资深中文编辑。请在保持原意与信息量的前提下，把下面的文字改写成通顺、专业、书面化的表达。\n" +
				"要求：\n1. 不添加原文没有的信息；\n2. 去掉口语化与冗余表达；\n3. 直接输出改写结果，不要解释。\n\n原文：\n{{input}}",
			Params: []SkillParam{{Key: "input", Label: "原文", Type: "textarea"}},
		},
		{
			Slug: "summarize", Name: "结构化摘要", Category: "文本", Icon: "list",
			Description: "把长文本压缩成带层级要点的摘要，便于快速浏览。",
			Capability:  CapChat, SortOrder: 30,
			Prompt: "请阅读下面的内容，输出结构化摘要。\n输出格式：\n## 一句话结论\n## 关键要点\n- 要点（不超过 6 条）\n## 待确认问题\n\n内容：\n{{input}}",
			Params: []SkillParam{{Key: "input", Label: "待摘要内容", Type: "textarea"}},
		},
		{
			Slug: "translate", Name: "中英互译", Category: "文本", Icon: "globe",
			Description: "在中文与英文之间互译，保留术语与格式。",
			Capability:  CapChat, SortOrder: 40,
			Prompt: "把下面的内容翻译成{{target}}。要求：保留原有的 Markdown 结构与专业术语，只输出译文。\n\n{{input}}",
			Params: []SkillParam{
				{Key: "input", Label: "原文", Type: "textarea"},
				{Key: "target", Label: "目标语言", Type: "select", Default: "英文", Options: []string{"英文", "中文"}},
			},
		},
		{
			Slug: "extract-json", Name: "结构化抽取", Category: "文本", Icon: "braces",
			Description: "按给定字段把自由文本抽取成严格 JSON。",
			Capability:  CapChat, SortOrder: 50,
			Prompt: "从下面的文本中抽取信息，输出严格合法的 JSON（不要包裹代码块、不要附加解释）。\n需要的字段：{{fields}}\n\n文本：\n{{input}}",
			Params: []SkillParam{
				{Key: "input", Label: "文本", Type: "textarea"},
				{Key: "fields", Label: "字段列表", Type: "text", Placeholder: "name, company, amount"},
			},
		},
		{
			Slug: "image-prompt", Name: "绘图提示词优化", Category: "图片", Icon: "image",
			Description: "把一句话想法扩写成结构化的文生图提示词。",
			Capability:  CapChat, SortOrder: 60,
			Prompt: "你是文生图提示词工程师。把下面的想法扩写成一条高质量提示词，包含主体、构图、光线、材质、镜头与风格。\n只输出提示词本身（英文），不要解释。\n\n想法：{{input}}",
			Params: []SkillParam{{Key: "input", Label: "想法", Type: "textarea"}},
		},
		{
			Slug: "storyboard", Name: "视频分镜脚本", Category: "视频", Icon: "film",
			Description: "把主题拆成可直接投喂视频模型的分镜提示词列表。",
			Capability:  CapChat, SortOrder: 70,
			Prompt: "请为主题「{{input}}」设计 {{shots}} 个镜头的分镜脚本。\n每个镜头输出：镜头号 / 画面描述（英文，适合视频生成模型）/ 时长（秒）/ 运镜方式。\n总时长控制在 {{duration}} 秒以内。",
			Params: []SkillParam{
				{Key: "input", Label: "主题", Type: "text"},
				{Key: "shots", Label: "镜头数", Type: "number", Default: "4"},
				{Key: "duration", Label: "总时长（秒）", Type: "number", Default: "20"},
			},
		},
		{
			Slug: "video-prompt", Name: "视频提示词增强", Category: "视频", Icon: "sparkles",
			Description: "把口语描述改写成视频模型易理解的英文提示词。",
			Capability:  CapChat, SortOrder: 80,
			Prompt: "把下面的描述改写成一条适合视频生成模型的英文提示词。包含：主体动作、镜头运动、环境光线、画面风格。\n只输出提示词本身。\n\n描述：{{input}}",
			Params: []SkillParam{{Key: "input", Label: "描述", Type: "textarea"}},
		},
		{
			Slug: "image-card", Name: "文生图直出", Category: "图片", Icon: "image",
			Description: "不经过对话模型，直接把提示词投给图片模型。",
			Capability:  CapImage, SortOrder: 90,
			Prompt: "{{input}}",
			Params: []SkillParam{
				{Key: "input", Label: "图片提示词", Type: "textarea"},
				{Key: "size", Label: "尺寸", Type: "select", Default: "1024x1024",
					Options: []string{"1024x1024", "1536x1024", "1024x1536", "512x512"}},
			},
		},
		{
			Slug: "video-card", Name: "文生视频直出", Category: "视频", Icon: "film",
			Description: "直接把提示词投给视频模型，创建生成任务。",
			Capability:  CapVideo, SortOrder: 100,
			Prompt: "{{input}}",
			Params: []SkillParam{
				{Key: "input", Label: "视频提示词", Type: "textarea"},
				{Key: "duration", Label: "时长（秒）", Type: "number", Default: "5"},
			},
		},
	}
}

func (s *Store) seedBuiltinSkills() error {
	for _, sk := range BuiltinSkills() {
		ts := now()
		_, err := s.db.Exec(`INSERT INTO skills
			(slug,name,description,category,icon,capability,prompt,params,builtin,enabled,sort_order,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,1,1,?,?,?)
			ON CONFLICT(slug) DO UPDATE SET
			  name=excluded.name, description=excluded.description, category=excluded.category,
			  icon=excluded.icon, capability=excluded.capability, prompt=excluded.prompt,
			  params=excluded.params, sort_order=excluded.sort_order, updated_at=excluded.updated_at`,
			sk.Slug, sk.Name, sk.Description, sk.Category, sk.Icon, sk.Capability,
			sk.Prompt, marshal(sk.Params), sk.SortOrder, ts, ts)
		if err != nil {
			return err
		}
	}
	return nil
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "skill-" + strings.Split(now(), "T")[1]
		out = strings.ReplaceAll(strings.ReplaceAll(out, ":", ""), "-", "")
	}
	return out
}
