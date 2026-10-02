package service

// 事件发布（[04 §4]）。
//
// **Service 不直接 import Wails**：业务层不该知道界面框架的存在（[04 §1.2] 的依赖方向、
// [12 §1.3] 的"任意 → 尽量少的外部依赖"）。装配层实现下面的接口，把它接到
// `runtime.EventsEmit`，业务侧只描述"发生了什么"。

// Publisher 是事件发布的出口，由装配层实现（[04 §4.1] 的极简模型）。
//
// 载荷刻意只有 ID：**Go 是唯一权威状态，事件只负责通知"东西变了"**，
// 前端收到后重新拉取（[04 §4.1]）。
type Publisher interface {
	// PlanChanged 表示某条计划变化，前端重新 PlanGet(id)。
	PlanChanged(id string)
	// PlansChanged 表示批量变化（创建、删除、恢复），前端重新 PlansList()。
	PlansChanged()
	// AppNotice 是用户可见提示（[04 §4.1] 的 appNotice：level / code / message）。
	AppNotice(level, code, message string)
}

// PlanViewPublisher 是**可选**的扩展能力。
//
// [04 §4.2] 对进度有例外规定：每 200 ms 推送一次**完整 PlanView**，前端直接覆盖本地记录，
// 不走"通知 + 重查"（那会产生大量往返）。装配层若实现了本接口，进度与终态推送就携带完整视图；
// 未实现时退化为 PlanChanged(id) + 前端重查。
//
// 用类型断言而不是把方法塞进 Publisher：装配层只实现三个方法就能跑通，
// 而想按 [04 §4.2] 做完整推送的装配层可以额外实现它。
type PlanViewPublisher interface {
	PlanProgress(view PlanView)
}

// publishPlan 通知某条计划发生变化。
func (r *planRuntime) publishPlan(view PlanView) {
	if r.pub == nil {
		return
	}
	if full, ok := r.pub.(PlanViewPublisher); ok {
		full.PlanProgress(view)
		return
	}
	r.pub.PlanChanged(view.ID)
}

// publishPlans 通知计划集合发生变化。
func (r *planRuntime) publishPlans() {
	if r.pub == nil {
		return
	}
	r.pub.PlansChanged()
}

// notice 发一条用户可见提示。消息必须可安全展示（[12 §3.3] 的 E3：不含路径、URL 或秘密）。
func (r *planRuntime) notice(level, code, message string) {
	if r.pub == nil {
		return
	}
	r.pub.AppNotice(level, code, message)
}
