package api

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"asset-mgr/integration/yunzhijia"
)

// TestNotifyStartupReportsOpenIDCoverage 证明「启动时报告 openId 覆盖率」这条诊断**真的被接上**。
//
// 为什么值得单独测：这条诊断的唯一价值就是在「打开了 todo_enabled 但没人有 openId」时
// 发出声音。若它没被接上（放错分支 / st 为 nil / 字段没配全导致 enabled 提前变 false），
// 表现与「接上了」完全一样——什么都不打印，系统照常启动。也就是说，
// **这条安全网本身失效时是无声的**，所以必须有用例证明它会响。
//
// 断言刻意只要求「覆盖率诊断出现过」，不绑定具体文案与数值：数值取决于库里真实数据，
// 绑死会让用例在数据变化时假红。这里要钉的是「接线」，不是「数据」。
func TestNotifyStartupReportsOpenIDCoverage(t *testing.T) {
	st, cfg := mustTestStore(t)

	// 配齐 enabled 的三个前置字段，否则 NewNotifyService 会把 enabled 强制为 false，
	// 覆盖率诊断所在的整个分支都不会进入——那样用例就成了空跑。
	cfg.Yunzhijia.TodoEnabled = true
	cfg.Yunzhijia.TodoAppID = "500000084"
	cfg.Yunzhijia.HeadImg = "https://img.example.com/head.png"
	cfg.Yunzhijia.PortalBaseURL = "https://fams.example.com:8448"
	// 同类解耦：本用例的反向对照断言「TodoEnabled=false ⇒ 未启用、不打覆盖率诊断」，而
	// enabled = (TodoEnabled || FlowCenterEnabled) && yzj != nil（notify.go:133）——
	// config.yaml 的 flowcenter_enabled 一旦为 true，反向对照里的 svc2 仍会 enabled、覆盖率诊断照打，
	// 用例假红。故与 notify_test.go 一致，显式钉死为 false。
	cfg.Yunzhijia.FlowCenterEnabled = false

	// 客户端只需非 nil（构造时不发请求），指向一个不存在的地址即可。
	yzj := yunzhijia.NewClient("http://127.0.0.1:1", "500000084", "test-secret", time.Second)

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	svc := NewNotifyService(st, cfg, yzj)
	t.Cleanup(func() { svc.Stop(time.Second) })

	out := buf.String()
	if !svc.enabled {
		t.Fatalf("前置条件不成立：待办未启用，覆盖率诊断分支不会进入。日志：\n%s", out)
	}
	if !strings.Contains(out, "openId") {
		t.Errorf("待办已启用，但启动日志里没有任何 openId 覆盖率诊断——"+
			"安全网没被接上，覆盖率为 0 时会静默发不出通知。日志：\n%s", out)
	}

	// 反向对照：未启用时不应打覆盖率诊断（避免无关噪音，也证明诊断确实挂在 enabled 分支内）。
	cfg.Yunzhijia.TodoEnabled = false
	buf.Reset()
	svc2 := NewNotifyService(st, cfg, yzj)
	t.Cleanup(func() { svc2.Stop(time.Second) })
	if strings.Contains(buf.String(), "openId 覆盖") {
		t.Errorf("待办未启用时不应打印覆盖率诊断。日志：\n%s", buf.String())
	}
}
