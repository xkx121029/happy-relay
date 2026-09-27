package store

import (
	"path/filepath"
	"strconv"
	"testing"
)

// TestSeedBuiltinPresets 验证内置免费平台播种：
// v4 起只保留「永久免费（无需充值）」的平台，播种只建渠道不建模型；
// 模型由配置向导在渠道测试通过后写入。用临时库跑，不依赖外网。
func TestSeedBuiltinPresets(t *testing.T) {
	// 预设本身先校验：全部永久免费、实名标记齐全、按量计费平台已剔除。
	presets := ModelPresets()
	if len(presets) == 0 {
		t.Fatal("内置预设为空")
	}
	for _, p := range presets {
		if !p.Free {
			t.Errorf("预设 %s 不是免费模型", p.Name)
		}
	}
	banned := map[string]bool{
		"DeepSeek": true, "Moonshot Kimi": true, "MiniMax": true,
		"阶跃星辰 StepFun": true, "阿里云百炼 DashScope": true, "火山方舟": true,
	}
	for _, p := range presets {
		if banned[p.ProviderName] {
			t.Errorf("平台 %s 非永久免费，不应出现在预设里", p.ProviderName)
		}
	}
	if !presets[0].RealnameRequired {
		t.Error("第一个预设（智谱）应标记需要实名认证")
	}

	// 播种行为校验：只建渠道，不建模型。
	dir := t.TempDir()
	db := filepath.Join(dir, "verify.db")
	s, err := Open(db)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer s.Close()

	providers, err := s.ListProviders()
	if err != nil {
		t.Fatalf("列出渠道失败：%v", err)
	}
	models, err := s.ListModels()
	if err != nil {
		t.Fatalf("列出模型失败：%v", err)
	}

	// 9 个永久免费平台：智谱 / 硅基流动 / 魔搭 / 千帆 / 讯飞 / OpenRouter /
	// NVIDIA NIM / Groq / 腾讯混元。
	const wantProviders = 9
	if len(providers) != wantProviders {
		t.Fatalf("内置渠道数量不符：got %d，期望 %d", len(providers), wantProviders)
	}
	if len(models) != 0 {
		t.Fatalf("播种不应创建模型（应由配置向导在测试通过后创建），实际 %d 个", len(models))
	}

	// GitHub Models 已从预设中移除：不应再播种出该渠道。
	for _, p := range providers {
		if p.Name == "GitHub Models" {
			t.Errorf("GitHub Models 已下架，不应再出现在内置渠道里")
		}
	}

	// 校验版本号已写入。
	if v := s.GetSetting(SettingBuiltinSeeded, "0"); v != strconv.Itoa(BuiltinSeedVersion) {
		t.Errorf("内置播种版本号应为 %d，实际 %s", BuiltinSeedVersion, v)
	}

	// 再次打开同一数据库：版本已达标，不应重复播种或清理。
	s2, err := Open(db)
	if err != nil {
		t.Fatalf("二次打开失败：%v", err)
	}
	defer s2.Close()
	providers2, _ := s2.ListProviders()
	if len(providers2) != wantProviders {
		t.Errorf("二次打开后渠道数量应不变：got %d，期望 %d", len(providers2), wantProviders)
	}
}
