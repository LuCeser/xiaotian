//go:build rtbrowser

// speech_fault_hooks.go — 浏览器测试的故障控制点（issue #69 备选一步骤 4）。
// 仅在 -tags=rtbrowser 测试构建中链接；生产（及其它标签）构建使用
// speech_fault_default.go 的内联 no-op，控制点在产物中不存在。
//
// 两类控制点，都打在生产代码的真实执行位置上：
//
//   - speechFaultInterceptSend：connWriter 的真实发送位置（SendControl 的
//     JSON 序列化之后、入队之前；SendAudio 的拷贝之前）。返回非 nil 错误
//     即拒绝该帧——后续步骤用它分别拒绝首句字幕/start/PCM/end 并验证
//     播报生命周期的失败语义。
//   - speechFaultPumpGateWait：connWriter 发送泵每帧写出前调用；安装 gate
//     后泵停滞（帧停在有界队列里，制造可观察的队列消费停滞/饱和）。
//
// 计时控制点复用 TurnManager 既有 timerFactory seam；供应商返回控制点在
// fake providers（providers_fake_test.go）上，均无需生产 hook。
package rt

import (
	"context"
	"sync"
)

var speechFaultState struct {
	mu sync.Mutex
	// send 拦截真实发送位置；nil 放行。kind 为 "control"/"audio"，payload
	// 对 control 是已序列化 JSON、对 audio 是裸 PCM。
	send func(kind string, payload []byte) error
	// sendHits 计 send 非 nil 期间的拦截调用次数（命中证据）。
	sendHits int
	// pumpGate 非 nil 时泵在每帧写出前取得放行信号；返回 nil 放行该帧。
	pumpGate func() <-chan struct{}
	// pumpWaits 计泵真实停在 gate 上的帧数（命中证据）。
	pumpWaits int
}

// speechFaultInterceptSend is the send-site hook conn.go calls.
func speechFaultInterceptSend(kind string, payload []byte) error {
	speechFaultState.mu.Lock()
	fn := speechFaultState.send
	if fn != nil {
		speechFaultState.sendHits++
	}
	speechFaultState.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(kind, payload)
}

// speechFaultPumpGateWait is the per-frame pump hook conn.go calls; the
// wait is interruptible by the writer's own lifecycle ctx (teardown never
// deadlocks on a held gate).
func speechFaultPumpGateWait(ctx context.Context) {
	speechFaultState.mu.Lock()
	fn := speechFaultState.pumpGate
	if fn != nil {
		speechFaultState.pumpWaits++
	}
	speechFaultState.mu.Unlock()
	if fn == nil {
		return
	}
	ch := fn()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

// speechFaultSetSend installs a send-site interceptor; the returned restore
// function is idempotent and safe for t.Cleanup.
func speechFaultSetSend(fn func(kind string, payload []byte) error) (restore func(), hits func() int) {
	speechFaultState.mu.Lock()
	speechFaultState.send = fn
	speechFaultState.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			speechFaultState.mu.Lock()
			speechFaultState.send = nil
			speechFaultState.mu.Unlock()
		})
	}, func() int {
		speechFaultState.mu.Lock()
		defer speechFaultState.mu.Unlock()
		return speechFaultState.sendHits
	}
}

// speechFaultSetPumpGate installs the pump consumption gate; restore is
// idempotent (t.Cleanup-safe).
func speechFaultSetPumpGate(fn func() <-chan struct{}) (restore func(), waits func() int) {
	speechFaultState.mu.Lock()
	speechFaultState.pumpGate = fn
	speechFaultState.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			speechFaultState.mu.Lock()
			speechFaultState.pumpGate = nil
			speechFaultState.mu.Unlock()
		})
	}, func() int {
		speechFaultState.mu.Lock()
		defer speechFaultState.mu.Unlock()
		return speechFaultState.pumpWaits
	}
}
