package watch

import (
	"time"
)

// 触发器时序常量（ADR-0010 §5，编译期常量施工可调）：
//   - 静默期：无新事件持续 1.5s 才触发——git 拉取/切分支一次写几百文件，
//     固定短窗口会切刀产生中间态扫描；
//   - 上限：本轮首个事件起 10s 内必触发——持续写入风暴不能永远压住变更。
const (
	QuiescePeriod = 1500 * time.Millisecond
	MaxWaitPeriod = 10 * time.Second
)

// triggerPhase 是触发器相位。
type triggerPhase int

const (
	// phaseIdle 无待决失效：下一事件重新进入 pending。
	phaseIdle triggerPhase = iota
	// phasePending 有待决失效：静默期满或上限到点即可触发。
	phasePending
	// phaseInflight 自动链进行中：新失效只标 dirty，本轮结束补一轮至干净
	//（与前端受控重查同型，风暴 ≤2 轮，ADR-0010 §5）。
	phaseInflight
)

// trigger 是单关系的触发器状态机：纯逻辑、不持通道、时间一律参数注入
//（假时钟单测=显式时间戳）。状态迁移由引擎驱动：invalidate（OS 事件派生的
// 失效）→ fireable/deadline（到点判定）→ start（发射链）→ settle（链收口）。
type trigger struct {
	quiesce time.Duration // 静默期（无新事件持续此时长才触发）
	maxWait time.Duration // 上限（本轮首个事件起最长等待强制触发）

	phase triggerPhase

	firstEvent time.Time // 本轮首个事件时刻（上限锚点；pending 期更新）
	lastEvent  time.Time // 最近事件时刻（静默期锚点）
	dirty      bool      // inflight 期间又有失效到达（本轮结束补一轮）
}

// newTrigger 构造触发器；时序取引擎注入值（生产=编译期常量，测试注入短值）。
func newTrigger(quiesce, maxWait time.Duration) trigger {
	return trigger{quiesce: quiesce, maxWait: maxWait}
}

// invalidate 记录一次失效（OS 事件聚合后的语义信号）。
//   - idle → pending：开启本轮计时（静默期 + 上限双锚点）；
//   - pending：刷新静默期锚点（上限锚点保持本轮首个事件）；
//   - inflight：只标 dirty，不开新轮（单飞）；
//   - 暂停态由引擎侧守卫（事件照常到达并记录，触发由引擎拦截）。
func (t *trigger) invalidate(now time.Time) {
	switch t.phase {
	case phaseInflight:
		t.dirty = true
	case phasePending:
		t.lastEvent = now
	default:
		t.phase = phasePending
		t.firstEvent = now
		t.lastEvent = now
	}
}

// deadline 返回本触发器的触发时刻与是否待决：静默期与上限取先到者。
// 非 pending 相位无可触发时刻。
func (t *trigger) deadline() (time.Time, bool) {
	if t.phase != phasePending {
		return time.Time{}, false
	}
	byQuiesce := t.lastEvent.Add(t.quiesce)
	byCap := t.firstEvent.Add(t.maxWait)
	if byCap.Before(byQuiesce) {
		return byCap, true
	}
	return byQuiesce, true
}

// fireable 判断 now 时刻是否已到触发点（静默期满或上限已到）。
func (t *trigger) fireable(now time.Time) bool {
	dl, ok := t.deadline()
	return ok && !now.Before(dl)
}

// start 发射自动链：pending → inflight（pending 事实由链消费）。
func (t *trigger) start() {
	t.phase = phaseInflight
	t.firstEvent = time.Time{}
	t.lastEvent = time.Time{}
}

// settle 链收口：flight 期间有失效 → 立即重开一轮 pending（补一轮至干净，
// 静默期从失效事件重算）；否则回 idle。
func (t *trigger) settle(now time.Time) {
	if t.dirty {
		t.dirty = false
		t.phase = phasePending
		t.firstEvent = now
		t.lastEvent = now
		return
	}
	t.phase = phaseIdle
}

// clear 清空待决失效（手动快速更新成功复位语境：链刚消费过全量扫描，
// 挂起的失效事实已随之消化）。
func (t *trigger) clear() {
	t.phase = phaseIdle
	t.firstEvent = time.Time{}
	t.lastEvent = time.Time{}
	t.dirty = false
}
