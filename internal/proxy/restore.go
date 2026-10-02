// Package proxy 负责系统代理的改写与恢复（[01 §6]）。
//
// 本阶段（D6）只实现**恢复方向**：启动第 2 步检查并回滚残留代理。
// 写入方向属阶段 5 的捕获流程（[01 §6.1]）。
//
// 恢复的三条硬性要求来自 [01 §6.3]：P1 排在启动第 2 步且不依赖数据库；
// P2 失败必须保留凭据文件；P4 **未持有凭据文件时绝不改写系统代理**。
package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	// FileName 是恢复凭据文件名，位于数据目录（[01 §8]）。
	FileName = "proxy-restore.json"
	// RestoreTimeout 是恢复的上限（[01 §5.3]：3 s，超限报错并提示用户手动检查）。
	RestoreTimeout = 3 * time.Second
)

// utf8BOM 是 UTF-8 字节顺序标记。凭据文件可能被编辑器或脚本写上它
// （实测：PowerShell 的 `Set-Content -Encoding UTF8` 就会写 BOM），
// 而 `json.Unmarshal` 不接受 BOM——不剥离就会把"文件可读"误判成"文件损坏"。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Settings 是系统代理的四项设置。
// [01 §5.3] 与 [11 `ST-2`] 点名了这四个值：回滚后必须与捕获前**逐值一致**。
type Settings struct {
	ProxyEnable   int    `json:"proxy_enable"`
	ProxyServer   string `json:"proxy_server"`
	ProxyOverride string `json:"proxy_override"`
	AutoConfigURL string `json:"auto_config_url"`
}

// Equal 逐值比较。恢复判据是"逐值一致"，不比较"看起来一样"。
func (s Settings) Equal(other Settings) bool { return s == other }

// Credential 是恢复凭据文件的内容（[01 §6.1]）。
//
// **同时保存 original 与 applied 是必要的**：恢复时要能判断"当前值是不是我们写的"。
type Credential struct {
	Original Settings `json:"original"`
	Applied  Settings `json:"applied"`
}

// Backend 是系统代理的读写后端。
//
// 生产实现走注册表（registry.go）；测试用内存实现——**测试绝不触碰真实系统代理**，
// 否则会把开发机断网（[12 §5.2] 的 T2：不污染用户环境）。
type Backend interface {
	Read() (Settings, error)
	Write(Settings) error
}

// Outcome 是恢复结果。
type Outcome string

const (
	// OutcomeNoFile：无凭据文件 → 不做任何操作（P4：绝不改写没凭据的代理）。
	OutcomeNoFile Outcome = "no_file"
	// OutcomeRestored：当前值 == applied → 已写回原值并删除凭据。
	OutcomeRestored Outcome = "restored"
	// OutcomeSkipped：当前值已被其他软件改过 → 不覆盖，仅记录；此时没有我们的改动需要回滚。
	OutcomeSkipped Outcome = "skipped"
	// OutcomeFailed：读/写失败 → **保留凭据文件**（P2），下次启动继续尝试。
	OutcomeFailed Outcome = "failed"
)

// Restore 执行 [01 §6.2] 的恢复流程：
//
//  1. 读凭据文件 → 不存在：无操作，结束
//  2. 读当前系统代理值
//  3. 当前值 == applied → 写回 original；否则不覆盖（其他软件已改），仅记录
//  4. 删除凭据文件
//
// 返回的 Outcome 用于日志；err 非空表示需要保留文件或提示用户。
func Restore(path string, backend Backend) (Outcome, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return OutcomeNoFile, nil
	}
	if err != nil {
		return OutcomeFailed, fmt.Errorf("读取代理恢复凭据失败: %w", err)
	}

	raw = bytes.TrimPrefix(raw, utf8BOM)

	var cred Credential
	if err := json.Unmarshal(raw, &cred); err != nil {
		// 凭据损坏：无法判断原值，保留文件并报错（P2）。
		return OutcomeFailed, fmt.Errorf("解析代理恢复凭据失败: %w", err)
	}

	current, err := backend.Read()
	if err != nil {
		return OutcomeFailed, fmt.Errorf("读取系统代理设置失败: %w", err)
	}

	if !current.Equal(cred.Applied) {
		// 其他软件已经改过代理：不覆盖（[01 §6.2] 第 3 步）。
		if err := removeQuietly(path); err != nil {
			return OutcomeSkipped, fmt.Errorf("清理代理恢复凭据失败: %w", err)
		}
		return OutcomeSkipped, nil
	}

	if err := backend.Write(cred.Original); err != nil {
		// 写回失败：保留凭据文件，交给下次启动重试（P2）。
		return OutcomeFailed, fmt.Errorf("恢复系统代理失败: %w", err)
	}
	if err := removeQuietly(path); err != nil {
		return OutcomeRestored, fmt.Errorf("清理代理恢复凭据失败: %w", err)
	}
	return OutcomeRestored, nil
}

func removeQuietly(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
