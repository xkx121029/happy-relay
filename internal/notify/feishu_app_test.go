package notify

import "testing"

// TestNewFeishuAppArgs 必填参数缺失时通道视为未配置。
func TestNewFeishuAppArgs(t *testing.T) {
	if NewFeishuApp("", "s", "oc") != nil {
		t.Fatal("缺 AppID 应返回 nil")
	}
	if NewFeishuApp("cli", "", "oc") != nil {
		t.Fatal("缺 Secret 应返回 nil")
	}
	if NewFeishuApp("cli", "s", "") != nil {
		t.Fatal("缺 ChatID 应返回 nil")
	}
	if NewFeishuApp("cli", "s", "oc_x") == nil {
		t.Fatal("参数齐全不应返回 nil")
	}
}

// TestFeishuCardStyle 告警类标题用红色头部，普通用蓝色；带 URL 时附打开面板按钮。
func TestFeishuCardStyle(t *testing.T) {
	card := feishuCard(Message{Title: "【Happy Relay】渠道持续不可用", Lines: []string{"· p1 失败率 80%"}})
	header := card["header"].(map[string]any)
	if header["template"] != "red" {
		t.Fatalf("告警标题应为红色头部，实际 %v", header["template"])
	}
	if len(card["elements"].([]map[string]any)) != 1 {
		t.Fatal("无 URL 不应有按钮")
	}

	card2 := feishuCard(Message{Title: "【Happy Relay】昨日用量日报", Lines: []string{"调用总数：10"}, URL: "http://127.0.0.1:8799"})
	header2 := card2["header"].(map[string]any)
	if header2["template"] != "blue" {
		t.Fatalf("普通标题应为蓝色头部，实际 %v", header2["template"])
	}
	if len(card2["elements"].([]map[string]any)) != 2 {
		t.Fatal("带 URL 应附按钮元素")
	}
}
