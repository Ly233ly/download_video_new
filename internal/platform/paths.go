// Package platform 是 Windows 平台封装：路径、单实例、命名事件。
// 上层只调用这里，不直接碰 syscall（[01 §3] 的依赖方向）。
package platform

import (
	"os"
	"path/filepath"
)

// DataDir 返回运行期数据目录：%LOCALAPPDATA%\LiudiDownloader（[01 §8]）。
//
// 注意：**不是**安装目录——安装目录是 %LOCALAPPDATA%\Programs\LiudiDownloader
// （[10 §8] 的 V1），两者刻意分开，否则"卸载保留数据"会误伤程序文件。
func DataDir() (string, error) {
	base, err := os.UserCacheDir() // Windows 上即 %LocalAppData%
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "LiudiDownloader"), nil
}

// DatabasePath 返回主库路径 %LOCALAPPDATA%\LiudiDownloader\data.db（[01 §8]）。
func DatabasePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "data.db"), nil
}

// LogDir 返回日志目录（D5 结构化日志使用）。
func LogDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

// ProxyRestorePath 返回代理恢复凭据路径（[01 §6]，D6 使用）。
func ProxyRestorePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "proxy-restore.json"), nil
}
