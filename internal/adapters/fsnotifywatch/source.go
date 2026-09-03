// Package fsnotifywatch 是目录监听事件源的 fsnotify adapter（ADR-0010 §10）：
// 单 fsnotify.Watcher 承载多目录注册，事件翻译为 ports.DirEvent 后转发。
// application 层只依赖 ports.DirEventSource 端口——core 不 import fsnotify
//（既有边界禁令）；测试以假事件源注入（缝②）。
package fsnotifywatch

import (
	"fmt"

	"github.com/fsnotify/fsnotify"

	"packgradle/internal/application/ports"
)

// Source 是 ports.DirEventSource 的 fsnotify 实现。
type Source struct {
	w      *fsnotify.Watcher
	events chan ports.DirEvent
	errors chan error
	done   chan struct{} // Close 关闭：让阻塞在转发上的 pump 退出（防 goroutine 泄漏）
}

// New 构造监听事件源（单 watcher 多目录）。构造失败（句柄/资源耗尽）返回错误，
// 调用方降级回手动（ADR-0010 §7：监听死活不影响快速更新可用性）。
func New() (*Source, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotifywatch: 创建 watcher: %w", err)
	}
	s := &Source{
		w:      w,
		events: make(chan ports.DirEvent),
		errors: make(chan error),
		done:   make(chan struct{}),
	}
	go s.pump()
	return s, nil
}

// Add 注册监听路径。
func (s *Source) Add(path string) error { return s.w.Add(path) }

// Remove 注销监听路径。
func (s *Source) Remove(path string) error { return s.w.Remove(path) }

// Events 返回事件通道。
func (s *Source) Events() <-chan ports.DirEvent { return s.events }

// Errors 返回错误通道。
func (s *Source) Errors() <-chan error { return s.errors }

// Close 停止转发并释放 watcher。
func (s *Source) Close() error {
	close(s.done)
	return s.w.Close()
}

// pump 把 fsnotify 事件翻译进端口通道；Close 后 fsnotify 通道关闭，转发退出。
// 转发是单 goroutine 串行的——下游引擎按事件到达序处理挂卸与触发。
func (s *Source) pump() {
	defer close(s.events)
	defer close(s.errors)
	for {
		select {
		case ev, ok := <-s.w.Events:
			if !ok {
				return
			}
			select {
			case s.events <- ports.DirEvent{Path: ev.Name, Op: translateOp(ev.Op)}:
			case <-s.done:
				return
			}
		case err, ok := <-s.w.Errors:
			if !ok {
				return
			}
			if err != nil {
				select {
				case s.errors <- err:
				case <-s.done:
					return
				}
			}
		}
	}
}

// translateOp 翻译操作位集。
func translateOp(op fsnotify.Op) ports.DirOp {
	var out ports.DirOp
	if op&fsnotify.Create != 0 {
		out |= ports.DirCreate
	}
	if op&fsnotify.Write != 0 {
		out |= ports.DirWrite
	}
	if op&fsnotify.Remove != 0 {
		out |= ports.DirRemove
	}
	if op&fsnotify.Rename != 0 {
		out |= ports.DirRename
	}
	if op&fsnotify.Chmod != 0 {
		out |= ports.DirChmod
	}
	return out
}
