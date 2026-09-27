package api

import "errors"

// errNoChannel 表示当前没有任何可用的通知通道。
// 面板在用户没配通道就点「发送测试」时据此给出明确提示，
// 而不是静默成功让用户以为配好了。
var errNoChannel = errors.New("尚未配置任何通知通道，请先填写 Webhook 或邮箱")
