// 端点健康缓存（会话级）：useEndpointPage 的健康结果原先挂在页面组件实例上，
// 路由切换即丢失，表现为「查过健康再回到页面就回到未检查」。提升为模块级
// 共享后，同一次运行内切页往返结果保留；应用重启后仍回到未检查——契约 03 §2.5
// 健康查询只读不落盘，持久化属契约变更不在此做。
// 键为端点 opaque ID（prj_/rtm_ 前缀全局唯一），项目源与运行实例共用一张表。
import { reactive } from 'vue'
import type { EndpointHealthDTO } from '../api'

export type EndpointHealthEntry = EndpointHealthDTO | 'checking'

const healthMap = reactive(new Map<string, EndpointHealthEntry>())

export function endpointHealthMap(): Map<string, EndpointHealthEntry> {
    return healthMap
}

// —— 写侧原语（调用方是 useEndpointPage；store 只管缓存，不持有服务与文案）——

// beginEndpointHealth 标记检查进行中（'checking' 哨兵；徽标显示检查中，
// 并让并发触发的同端点检查自然去重）
export function beginEndpointHealth(id: string): void {
    healthMap.set(id, 'checking')
}

// setEndpointHealth 落位一次健康检查结果；会话内切页往返直接命中，
// 不再重复请求（hasEntry 判断缓存命中的唯一依据）
export function setEndpointHealth(id: string, dto: EndpointHealthDTO): void {
    healthMap.set(id, dto)
}

// clearEndpointHealth 清除条目（检查失败回退「未检查」态，错误提示由调用方负责）
export function clearEndpointHealth(id: string): void {
    healthMap.delete(id)
}

// endpointHealthOf 读健康结果；'checking' 哨兵视为无结果（供模板收窄成 DTO）
export function endpointHealthOf(id: string): EndpointHealthDTO | undefined {
    const entry = healthMap.get(id)
    return entry && entry !== 'checking' ? entry : undefined
}
