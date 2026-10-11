package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-demo/lessons/m19_composition_architecture/internal/config"
)

// TestConfigurationSources 独立检查文件/env/default 的有效结果，未创建任何服务器。
// lookup 读取测试 map，使环境覆盖只属于本次 Load，不污染其他课程/并行测试的进程环境。
func TestConfigurationSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"token":"file-secret","version":"file-version","request_timeout_ms":120}`), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"M19_TOKEN": "env-secret", "M19_VERSION": "env-version"}
	cfg, err := config.Load(path, func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "env-secret" || cfg.Version != "env-version" || cfg.Listen != "127.0.0.1:0" || cfg.RequestMS != 120 {
		t.Fatalf("source priority: %+v", cfg.Safe())
	}
	safe, err := json.Marshal(cfg.Safe())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(safe), "secret") || strings.Contains(string(safe), "token") {
		t.Fatalf("secret exposure: %s", safe)
	}
}

// TestConfigurationRejectsBeforeResources 检查结构/大小/必需项，不把解析成功当有效配置。
// 文件用 t.TempDir 归测试 owner；每项写完后 Load 内部的文件 defer 都应已执行。
func TestConfigurationRejectsBeforeResources(t *testing.T) {
	for _, item := range []struct{ name, body string }{
		{"missing-token", `{}`}, {"unknown-field", `{"token":"secret","dsn":"secret"}`},
		{"trailing", `{"token":"secret"} {}`}, {"zero-budget", `{"token":"secret","request_timeout_ms":0}`},
		{"public-listen", `{"token":"secret","listen":"0.0.0.0:0"}`}, {"oversized", strings.Repeat(" ", 4097)},
		{"bad-port", `{"token":"secret","listen":"127.0.0.1:70000"}`},
	} {
		t.Run(item.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(item.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(path, func(string) (string, bool) { return "", false })
			if err == nil {
				t.Fatal("invalid config was accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaked content: %v", err)
			}
		})
	}
	_, err := config.Load("", func(key string) (string, bool) {
		if key == "M19_TOKEN" {
			return "secret", true
		}
		if key == "M19_REQUEST_TIMEOUT_MS" {
			return "secret-not-number", true
		}
		return "", false
	})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("environment error unsafe: %v", err)
	}
}
