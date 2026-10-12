// yzjflowinit 手动执行一次云之家统一流程中心模板初始化（/gateway/flowcenter/native/init）。
//
// 为什么是「手动小工具」而不是「启动时自动调」：
//   - init 的 field 是**累加**语义——对同一 typeId 多次初始化会**追加**字段而非覆盖，
//     每次启动自动调会持续污染云之家侧的模板；
//   - 官方「必读」第 1 条：集成上线后**请勿再修改集成参数，尤其是 AppId**，
//     否则流程中心待办可能无法正常处理。初始化是「一次性、需人工确认」的动作，故独立成工具。
//
// P0 **不注册自定义字段**（Field 留空）：send 的 field 仅首次生效，且不注册也能正常收发待办；
// title + messageContent 已足够。需要「重要字段」展示时再改这里并重跑（注意累加语义）。
//
// 用法（凭证走环境变量，与主程序一致：config.yaml 里 app_id/secret/eid 可留空靠 env 覆盖）：
//
//	YZJ_APP_ID=xxx YZJ_SECRET=yyy YZJ_EID=10603457 \
//	  go run ./cmd/yzjflowinit -config config.yaml -creator <发起人openId>
//
// 退出码：0 = 成功；1 = 配置缺失 / 调用失败。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/yunzhijia"
)

const (
	exitOK   = 0
	exitFail = 1
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件")
	// creator（模板创建人 openId）在 init 里是**必填**。默认取 sender_id，也可命令行覆盖。
	creator := flag.String("creator", "", "模板创建人 openId（缺省取 yunzhijia.sender_id）")
	timeout := flag.Duration("timeout", 30*time.Second, "接口调用超时")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	yz := cfg.Yunzhijia

	// 前置检查：缺任何一项都跑不起来，先报清楚「怎么修」，别去打网络。
	var missing []string
	if yz.BaseURL == "" {
		missing = append(missing, "yunzhijia.base_url")
	}
	if yz.AppID == "" {
		missing = append(missing, "yunzhijia.app_id（或环境变量 YZJ_APP_ID）")
	}
	if yz.Secret == "" {
		missing = append(missing, "yunzhijia.secret（或环境变量 YZJ_SECRET）")
	}
	if yz.EID == "" {
		missing = append(missing, "yunzhijia.eid（或环境变量 YZJ_EID；流程中心 team token 必填）")
	}
	if yz.FlowCenterTypeID == "" {
		missing = append(missing, "yunzhijia.flowcenter_type_id（模板标识，自定任意值，本项目取 WXSQD）")
	}
	if yz.FlowCenterTypeName == "" {
		missing = append(missing, "yunzhijia.flowcenter_type_name（模板名，如 维修工单）")
	}
	if yz.FlowCenterAppName == "" {
		missing = append(missing, "yunzhijia.flowcenter_app_name（模板分类名，一般填轻应用名）")
	}
	creatorOpenID := *creator
	if creatorOpenID == "" {
		creatorOpenID = yz.SenderID
	}
	if creatorOpenID == "" {
		missing = append(missing, "creator（-creator 参数或 yunzhijia.sender_id，必须是 云之家 openId）")
	}
	if len(missing) > 0 {
		fmt.Println("配置缺失，无法初始化流程中心模板：")
		for _, m := range missing {
			fmt.Printf("  - %s\n", m)
		}
		fmt.Println("\n提示：flowcenter_type_id 是**自定任意值**，init 之前可自由改；一旦开始发待办就不能再改")
		fmt.Println("      （约束来自 send 接口「同一流程每次发送待办都用相同 typeId」）。")
		fmt.Println("      官方「必读」第 1 条约束的是 AppId，不是 typeId——别混为一谈。")
		os.Exit(exitFail)
	}

	client := yunzhijia.NewClient(yz.BaseURL, yz.AppID, yz.Secret, *timeout)
	client.SetEID(yz.EID) // 流程中心走 team 级 accessToken，必须带 eid

	appID := yz.TodoAppID
	if appID == "" {
		appID = yz.AppID
	}
	req := &yunzhijia.InitFlowRequest{
		TypeID:      yz.FlowCenterTypeID,
		TypeName:    yz.FlowCenterTypeName,
		AppID:       appID,
		AppName:     yz.FlowCenterAppName,
		WebURL:      yz.FlowCenterWebURL,
		MobileURL:   yz.FlowCenterMobileURL,
		Field:       nil, // P0 不注册自定义字段（见文件头说明）
		Creator:     creatorOpenID,
		Description: "FAMS 固定资产管理系统 —— 维修工单流程（原生集成）",
	}

	log.Printf("正在初始化流程中心模板：typeId=%s typeName=%s appId=%s creator=%s",
		yz.FlowCenterTypeID, yz.FlowCenterTypeName, appID, yunzhijia.MaskOpenID(creatorOpenID))

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res, err := client.InitFlowTemplate(ctx, req)
	if err != nil {
		log.Fatalf("流程中心初始化调用失败: %v", err)
	}
	if res == nil || !res.OK() {
		code, msg := 0, ""
		if res != nil {
			code, msg = res.ErrorCode, res.Error
		}
		log.Fatalf("流程中心初始化被拒绝: errorCode=%d error=%s", code, msg)
	}
	fmt.Printf("流程中心模板初始化成功：typeId=%s（发过待办后不可再改；init 之前可自由改）\n", yz.FlowCenterTypeID)
	os.Exit(exitOK)
}
