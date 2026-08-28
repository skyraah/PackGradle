// Package application 及其子包承载新架构的应用用例层：
// 用例编排（sync）、任务生命周期（task）、视图投影（view）、策略模板（policy）
// 以及供外围实现的 ports 接口定义。
//
// 边界约束：
//   - application 只通过 ports 接口消费适配器与存储，不直接 import 具体实现
//     （唯一例外是装配构造器与 main.go）；
//   - application 不 import Wails；DTO 转换属于 transport 职责。
package application
