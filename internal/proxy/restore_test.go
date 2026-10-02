package proxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// memBackend 是内存后端：**测试绝不触碰真实系统代理**（否则会把开发机断网）。
type memBackend struct {
	s        Settings
	writeErr error
	writes   int
}

func (m *memBackend) Read() (Settings, error) { return m.s, nil }

func (m *memBackend) Write(s Settings) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.s = s
	m.writes++
	return nil
}

var (
	original = Settings{ProxyEnable: 0, ProxyServer: "", ProxyOverride: "", AutoConfigURL: ""}
	applied  = Settings{ProxyEnable: 1, ProxyServer: "127.0.0.1:47653", ProxyOverride: "<local>", AutoConfigURL: ""}
)

func writeCred(t *testing.T, dir string, cred Credential) string {
	t.Helper()
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("序列化凭据失败: %v", err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入凭据失败: %v", err)
	}
	return path
}

// 无凭据文件时绝不改写系统代理（[01 §6.3] 的 P4）。
func TestRestore_NoFile_DoesNotTouchProxy(t *testing.T) {
	backend := &memBackend{s: original}

	outcome, err := Restore(filepath.Join(t.TempDir(), FileName), backend)
	if err != nil {
		t.Fatalf("Restore 返回错误: %v", err)
	}
	if outcome != OutcomeNoFile {
		t.Errorf("outcome = %q，期望 %q", outcome, OutcomeNoFile)
	}
	if backend.writes != 0 {
		t.Error("无凭据文件时不应写代理")
	}
}

// 当前值 == applied：写回原值并删除凭据（[01 §6.2] 第 3、4 步）。
func TestRestore_RestoresOriginal(t *testing.T) {
	path := writeCred(t, t.TempDir(), Credential{Original: original, Applied: applied})
	backend := &memBackend{s: applied}

	outcome, err := Restore(path, backend)
	if err != nil {
		t.Fatalf("Restore 返回错误: %v", err)
	}
	if outcome != OutcomeRestored {
		t.Errorf("outcome = %q，期望 %q", outcome, OutcomeRestored)
	}
	if !backend.s.Equal(original) {
		t.Errorf("代理未写回原值: %+v", backend.s)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("恢复成功后应删除凭据文件")
	}
}

// 当前值已被其他软件改过：不覆盖（B-719、[01 §6.2] 第 3 步）。
func TestRestore_SkipsWhenOthersChanged(t *testing.T) {
	path := writeCred(t, t.TempDir(), Credential{Original: original, Applied: applied})
	others := Settings{ProxyEnable: 1, ProxyServer: "127.0.0.1:7890"}
	backend := &memBackend{s: others}

	outcome, err := Restore(path, backend)
	if err != nil {
		t.Fatalf("Restore 返回错误: %v", err)
	}
	if outcome != OutcomeSkipped {
		t.Errorf("outcome = %q，期望 %q", outcome, OutcomeSkipped)
	}
	if !backend.s.Equal(others) {
		t.Errorf("不得覆盖其他软件写入的值: %+v", backend.s)
	}
	if backend.writes != 0 {
		t.Error("跳过时不应产生任何写入")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("跳过时凭据文件也应清理——已经没有我们的改动需要回滚")
	}
}

// 写回失败必须保留凭据文件，交给下次启动重试（P2）。
func TestRestore_KeepsFileOnWriteFailure(t *testing.T) {
	path := writeCred(t, t.TempDir(), Credential{Original: original, Applied: applied})
	backend := &memBackend{s: applied, writeErr: errors.New("拒绝访问")}

	outcome, err := Restore(path, backend)
	if err == nil {
		t.Fatal("写回失败时应返回错误")
	}
	if outcome != OutcomeFailed {
		t.Errorf("outcome = %q，期望 %q", outcome, OutcomeFailed)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("失败时必须保留凭据文件（P2）")
	}
}

// 凭据损坏：保留文件并报错，绝不猜测原值。
func TestRestore_CorruptCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("写入损坏凭据失败: %v", err)
	}
	backend := &memBackend{s: applied}

	outcome, err := Restore(path, backend)
	if err == nil {
		t.Fatal("凭据损坏时应返回错误")
	}
	if outcome != OutcomeFailed {
		t.Errorf("outcome = %q，期望 %q", outcome, OutcomeFailed)
	}
	if backend.writes != 0 {
		t.Error("凭据损坏时不得写代理")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("损坏的凭据应保留以便人工检查")
	}
}

// 凭据文件带 UTF-8 BOM 时仍应正常解析。
// 实测来源：PowerShell 的 `Set-Content -Encoding UTF8` 会写 BOM，若不容忍，
// 会把"文件其实可读"误判成"文件损坏"并永久保留（P2），恢复就再也做不成了。
func TestRestore_ToleratesBOM(t *testing.T) {
	body, err := json.Marshal(Credential{Original: original, Applied: applied})
	if err != nil {
		t.Fatalf("序列化凭据失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, body...), 0o600); err != nil {
		t.Fatalf("写入带 BOM 的凭据失败: %v", err)
	}
	backend := &memBackend{s: applied}

	outcome, err := Restore(path, backend)
	if err != nil {
		t.Fatalf("带 BOM 的凭据应当可解析: %v", err)
	}
	if outcome != OutcomeRestored {
		t.Errorf("outcome = %q，期望 %q", outcome, OutcomeRestored)
	}
	if !backend.s.Equal(original) {
		t.Errorf("代理未写回原值: %+v", backend.s)
	}
}

// 恢复判据是"逐值一致"，不是结构体非零比较（[11 `ST-2`]）。
func TestSettings_EqualIsPerValue(t *testing.T) {
	a := Settings{ProxyEnable: 1, ProxyServer: "x", ProxyOverride: "y", AutoConfigURL: "z"}
	b := a
	if !a.Equal(b) {
		t.Error("相同值应判定相等")
	}
	b.AutoConfigURL = "w"
	if a.Equal(b) {
		t.Error("任一字段不同即不相等")
	}
}
