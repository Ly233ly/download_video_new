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
)

// Deps 是 Service 的依赖。Store 必需；Tools 在缺少媒体工具时可为"部分可用"的集合。
type Deps struct {
	Store *store.DB
	Tools *media.Toolset
}

// Service 持有全部业务规则。
type Service struct {
	store *store.DB
	tools *media.Toolset
}

// New 装配 Service。缺少 Store 属于装配错误：启动期直接失败，不留半成品（[12 §3.2]）。
func New(deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("Service 需要数据库依赖")
	}
	return &Service{store: deps.Store, tools: deps.Tools}, nil
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
