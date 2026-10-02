package proxy

import (
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// internetSettingsKey 是当前用户的系统代理设置位置。
// 只操作 HKCU：免管理员，且不碰机器级设置（[10 §7] 的同类边界）。
const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// 四项设置名，与 Settings 的字段一一对应。
const (
	valueProxyEnable   = "ProxyEnable"
	valueProxyServer   = "ProxyServer"
	valueProxyOverride = "ProxyOverride"
	valueAutoConfigURL = "AutoConfigURL"
)

// RegistryBackend 是真实的系统代理后端。
type RegistryBackend struct{}

// Read 读取当前代理设置。缺失的值按零值处理——"没有设置"与"设置为空"对本程序等价。
func (RegistryBackend) Read() (Settings, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return Settings{}, err
	}
	defer func() { _ = key.Close() }()

	var s Settings
	if v, _, err := key.GetIntegerValue(valueProxyEnable); err == nil {
		s.ProxyEnable = int(v)
	}
	if v, _, err := key.GetStringValue(valueProxyServer); err == nil {
		s.ProxyServer = v
	}
	if v, _, err := key.GetStringValue(valueProxyOverride); err == nil {
		s.ProxyOverride = v
	}
	if v, _, err := key.GetStringValue(valueAutoConfigURL); err == nil {
		s.AutoConfigURL = v
	}
	return s, nil
}

// Write 写回四项设置，并通知系统立即重新读取代理配置。
func (RegistryBackend) Write(s Settings) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = key.Close() }()

	if err := key.SetDWordValue(valueProxyEnable, uint32(s.ProxyEnable)); err != nil {
		return err
	}
	if err := key.SetStringValue(valueProxyServer, s.ProxyServer); err != nil {
		return err
	}
	if err := key.SetStringValue(valueProxyOverride, s.ProxyOverride); err != nil {
		return err
	}
	if err := key.SetStringValue(valueAutoConfigURL, s.AutoConfigURL); err != nil {
		return err
	}

	notifySettingsChanged()
	return nil
}

var (
	wininet                       = syscall.NewLazyDLL("wininet.dll")
	procInternetSetOptionW        = wininet.NewProc("InternetSetOptionW")
	internetOptionSettingsChanged = 39 // INTERNET_OPTION_SETTINGS_CHANGED
	internetOptionRefresh         = 37 // INTERNET_OPTION_REFRESH
)

// notifySettingsChanged 让系统立刻应用注册表里的新代理设置。
//
// 不改这一步的话，注册表虽然变了，但已有进程仍按旧代理工作——对"恢复代理、
// 让用户马上能上网"这个目标来说等于没恢复（[11 `ST-2`] 要求回滚后与捕获前逐值一致，
// 而"一致"必须对用户实际生效）。
//
// 失败不返回错误：系统会在下次网络状态变化时自行读取；返回错误反而会掩盖
// "注册表已写好"这个事实。
func notifySettingsChanged() {
	for _, opt := range []uintptr{uintptr(internetOptionSettingsChanged), uintptr(internetOptionRefresh)} {
		_, _, _ = procInternetSetOptionW.Call(0, opt, 0, 0)
	}
}
