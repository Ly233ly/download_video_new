// Package service 是**唯一业务入口**（[04 §1.2]）：Wails 绑定层与本地 HTTP handler
// 都必须调用它，**不得各写一套业务逻辑**。
//
//	UI / Extension → (Wails Binding | HTTP Handler) → Service → Store / Media / Eagle
//
// 本阶段（D4）只建立骨架与依赖装配，业务方法随各阶段加入。
// **刻意不引入** Clean Architecture 的分层（domain/repository/usecase/gateway）——
// [04 §1.2] 明确说"一个 Service 层足够"。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Ly233ly/download_video_new/internal/media"
	"github.com/Ly233ly/download_video_new/internal/store"
	"time"
)

// Deps 是 Service 的依赖。Store 必需；其余都是**可选**的，缺省时对应能力关闭，
// 而不是假装可用（[12 §3.3] 的"不静默降级"）。
type Deps struct {
	Store *store.DB
	Tools *media.Toolset

	// Runner 是下载引擎（[05 §4]）。为 nil 时**自建默认引擎**（[internal/media] 的直链实现）——
	// 装配层因此不必知道引擎的构造细节；想换参数（HTTP 客户端、工具超时等）时再显式注入。
	Runner Runner
	// Publisher 把状态变化发给界面（[04 §4]）。装配层负责接到 Wails 的 EventsEmit。
	Publisher Publisher
	// Eagle 是 Eagle 可用性探测（阶段 4）。为 nil 时视为不可用（[02 B-211]：禁用补导入口）。
	Eagle EagleProbe
	// Resolver 解析主机名，用于创建期的内网目标判定（[03 §4.3]）。为 nil 时用系统解析器。
	Resolver HostResolver
	// Now 注入时钟，供测试固定时间（[12 §5.2] 的 T4）。为 nil 时用 time.Now。
	Now func() time.Time
	// Version 是产品版本，供健康接口展示。由装配层注入——版本常量的权威位置在 app 包，
	// service 不能反向依赖它（[12 §1.3] 禁止循环依赖）。
	Version string
}

// Service 持有全部业务规则。
type Service struct {
	store *store.DB
	tools *media.Toolset
	// plan 是计划业务与调度的运行时状态（[05]）。集中成一个结构，
	// 让本骨架在后续阶段加入能力时不必反复改动字段。
	plan *planRuntime
}

// New 装配 Service。缺少 Store 属于装配错误：启动期直接失败，不留半成品（[12 §3.2]）。
//
// 未注入 Runner 时用 [internal/media] 的直链引擎自建：装配层（main/app）只需给出
// Store 与 Tools 就能让计划真的跑起来，不必知道引擎怎么构造。
func New(deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("Service 需要数据库依赖")
	}
	if deps.Runner == nil {
		// 引擎构造失败只可能是选项非法（[internal/media] 的 New 语义），
		// 属启动期装配错误——直接失败而不是留一个"计划永远不动"的半成品。
		engine, err := NewDownloadRunner(deps.Tools, media.Options{})
		if err != nil {
			return nil, fmt.Errorf("构造下载引擎失败: %w", err)
		}
		deps.Runner = engine
	}
	return &Service{
		store: deps.Store,
		tools: deps.Tools,
		plan:  newPlanRuntime(deps),
	}, nil
}

// Setting 读取字符串配置。
//
// 两条回退规则都来自 [03 §2.4]：
//   - 键不存在 → 返回 fallback；
//   - 值存在但**不是字符串 JSON** → 同样返回 fallback。
//
// 两者都**不报错**：解码失败不得中断启动。
func (s *Service) Setting(ctx context.Context, key, fallback string) (string, error) {
	raw, ok, err := s.store.GetSetting(ctx, key)
	if err != nil {
		return fallback, err
	}
	if !ok {
		return fallback, nil
	}

	var decoded string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return fallback, nil
	}
	return decoded, nil
}

// SetSetting 写入字符串配置（内部按 [03 §2.4] 的"value 一律 JSON 编码"处理）。
func (s *Service) SetSetting(ctx context.Context, key, value string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("编码配置值失败: %w", err)
	}
	return s.store.SetSetting(ctx, key, string(encoded))
}

// Tools 返回随包媒体工具的只读快照（供界面与诊断展示）。
func (s *Service) Tools() []media.Tool {
	if s.tools == nil {
		return nil
	}
	return s.tools.All()
}
