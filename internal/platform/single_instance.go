package platform

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// 命名对象：单实例互斥体与唤醒事件（[01 §1.2]、[01 §2] 的 C5）。
//
// 命名形式统一为反斜杠的 `Local\...`。旧版用的是 `Local_IdmEagleAutoImport`
// （见 [baseline.md §4]），新版不复用旧名——旧名属于另一个程序的身份。
const (
	singleInstanceMutex = `Local\LiudiDownloader.SingleInstance` // B-1002
	wakeEventName       = `Local\LiudiWake`                      // 重复启动与 IDM hook 共用
)

const (
	errAlreadyExists      = syscall.Errno(183) // ERROR_ALREADY_EXISTS
	waitObject0           = 0                  // WAIT_OBJECT_0
	waitPollTimeoutMillis = 1000               // 轮询上限，保证等待可取消
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW        = kernel32.NewProc("CreateMutexW")
	procCreateEventW        = kernel32.NewProc("CreateEventW")
	procSetEvent            = kernel32.NewProc("SetEvent")
	procWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
)

// Instance 持有单实例所有权与唤醒事件。
type Instance struct {
	mutex syscall.Handle
	wake  syscall.Handle
}

// Acquire 取得单实例所有权（[01 §5.1] 启动第 1 步）：
//
//   - (inst, true, nil)  —— 本进程是第一个实例，继续启动。
//   - (nil, false, nil) —— 已有实例在运行，**已向它发送唤醒事件**，本进程应立即退出（T-STB-02）。
//   - (nil, false, err)  —— 平台调用失败。
func Acquire() (*Instance, bool, error) {
	namePtr, err := syscall.UTF16PtrFromString(singleInstanceMutex)
	if err != nil {
		return nil, false, fmt.Errorf("创建单实例互斥体失败: %w", err)
	}
	h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return nil, false, fmt.Errorf("创建单实例互斥体失败: %w", callErr)
	}
	if errors.Is(callErr, errAlreadyExists) {
		procCloseHandle.Call(h)
		if wakeErr := Wake(); wakeErr != nil {
			return nil, false, wakeErr
		}
		return nil, false, nil
	}

	wakePtr, err := syscall.UTF16PtrFromString(wakeEventName)
	if err != nil {
		procCloseHandle.Call(h)
		return nil, false, fmt.Errorf("创建唤醒事件失败: %w", err)
	}
	ev, _, callErr := procCreateEventW.Call(0, 0, 0, uintptr(unsafe.Pointer(wakePtr)))
	if ev == 0 {
		procCloseHandle.Call(h)
		return nil, false, fmt.Errorf("创建唤醒事件失败: %w", callErr)
	}

	return &Instance{mutex: syscall.Handle(h), wake: syscall.Handle(ev)}, true, nil
}

// Wake 触发唤醒事件：重复启动的第二个实例调用它来唤起已有实例；
// IDM hook 亦发送同名事件（[01 §1.2]）。
func Wake() error {
	namePtr, err := syscall.UTF16PtrFromString(wakeEventName)
	if err != nil {
		return fmt.Errorf("发送唤醒事件失败: %w", err)
	}
	h, _, callErr := procCreateEventW.Call(0, 0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return fmt.Errorf("发送唤醒事件失败: %w", callErr)
	}
	defer procCloseHandle.Call(h)
	if r, _, callErr := procSetEvent.Call(h); r == 0 {
		return fmt.Errorf("发送唤醒事件失败: %w", callErr)
	}
	return nil
}

// Wait 阻塞等待唤醒信号，ctx 取消即返回；每次收到信号调用一次 onWake。
//
// 采用「1 秒超时 + 循环」而非无限等待：规范禁止不可取消的等待与无主 goroutine
// （[01 §4]、[12 §9] 的 C1/C2）。1 秒同时满足 [01 §4.1] 对兜底定时器"不小于 1 秒"的要求。
func (i *Instance) Wait(ctx context.Context, onWake func()) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if r, _, _ := procWaitForSingleObject.Call(uintptr(i.wake), waitPollTimeoutMillis); r == waitObject0 {
			if onWake != nil {
				onWake()
			}
		}
	}
}

// Close 释放句柄；释放互斥体即允许下一个实例取得所有权。
func (i *Instance) Close() {
	if i.wake != 0 {
		procCloseHandle.Call(uintptr(i.wake))
		i.wake = 0
	}
	if i.mutex != 0 {
		procCloseHandle.Call(uintptr(i.mutex))
		i.mutex = 0
	}
}
