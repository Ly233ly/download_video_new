package proxy

import (
	"os"
	"testing"
)

// 读写回环：把读到的代理值原样写回，再读一次必须**逐值一致**。
//
// 这条验证用来回答一个具体问题："恢复后代理值变了，是我们的写入不忠实，
// 还是别的程序（如 FlClash）在同时改代理？"
//
// **默认跳过**：它会真实读写当前用户的系统代理注册表。虽然写回的是同一份值
// （不改变语义），仍不应在常规测试里默默执行——用 `LIUDI_PROXY_RT=1` 显式开启：
//
//	$env:LIUDI_PROXY_RT='1'; go test ./internal/proxy/ -run RoundTrip -v
func TestRegistryBackend_RoundTrip(t *testing.T) {
	if os.Getenv("LIUDI_PROXY_RT") != "1" {
		t.Skip("需要 LIUDI_PROXY_RT=1：会真实读写系统代理注册表")
	}

	backend := RegistryBackend{}

	before, err := backend.Read()
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if err := backend.Write(before); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	after, err := backend.Read()
	if err != nil {
		t.Fatalf("第二次 Read 失败: %v", err)
	}

	if !before.Equal(after) {
		t.Errorf("读写回环不一致——说明写入不忠实：\n  before = %+v\n  after  = %+v", before, after)
	}
	t.Logf("回环一致: %+v", after)
}
