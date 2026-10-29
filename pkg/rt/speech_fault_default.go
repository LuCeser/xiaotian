//go:build !rtbrowser

// speech_fault_default.go — 生产构建的故障控制点 no-op。真实实现只在
// -tags=rtbrowser 测试构建中存在（speech_fault_hooks.go）；这里内联为空，
// 保证控制点在产物二进制中不出现。
package rt

import "context"

func speechFaultInterceptSend(string, []byte) error { return nil }

func speechFaultPumpGateWait(context.Context) {}
