package lesson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestConfigRejectsBeforeCredentials 验证授权入口与秘密错误的边界，不连接 Redis。
// 凭据路径故意不存在，非法host必须先被拒绝，不能靠后面的ReadFile失败偶然挡住拨号。
func TestConfigRejectsBeforeCredentials(t *testing.T) {
	for _, host := range []string{"example.com", "localhost", "::1", "10.0.0.1"} {
		t.Run(host, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.json")
			data := fmt.Sprintf(`{"services":{"redis":{"state":"READY","host":%q,"port":6379,"server_version":"8.2.8","credential_file":"missing.json"}}}`, host)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("非法host没有在秘密读取之前拒绝：%v", err)
			}
		})
	}
}

// TestConfigSecretIsNotReported 用合成秘密核验序列化与错误消息不包含正文。
// 临时目录由testing拥有；这只证明配置代码的安全边界，不冒充实库AUTH。
func TestConfigSecretIsNotReported(t *testing.T) {
	dir := t.TempDir()
	index, secret := filepath.Join(dir, "index.json"), filepath.Join(dir, "secret.json")
	if err := os.WriteFile(index, []byte(`{"services":{"redis":{"state":"READY","host":"127.0.0.1","port":6379,"server_version":"8.2.8","credential_file":"secret.json"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	const syntheticSecret = "synthetic-password-never-print"
	if err := os.WriteFile(secret, []byte(`{"username":"synthetic-user","password":"`+syntheticSecret+`","db":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(index)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), syntheticSecret) || strings.Contains(string(data), "synthetic-user") {
		t.Fatalf("Config序列化泄露秘密或失败：error=%v", err)
	}
	if err := os.WriteFile(secret, []byte(`{"password":"`+syntheticSecret+`" invalid}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadConfig(index)
	if err == nil || strings.Contains(err.Error(), syntheticSecret) {
		t.Fatal("错误应拒绝秘密JSON且不含正文")
	}
}

// TestEarlyOwnerFailureJoinsAll 是无网络的任务生命周期断言：owner中途失败也停止并join两个任务。
// 这里不检验Redis；真实借池/连接失败的收尾在 TestLiveEarlyFailure 中独立验证。
func TestEarlyOwnerFailureJoinsAll(t *testing.T) {
	var exited atomic.Int32
	var report Report
	wantErr := errors.New("synthetic owner failed before normal wait")
	err := func() (err error) {
		g := newTaskGroup(context.Background(), &report)
		defer g.finish(&err)
		for range 2 {
			g.start(func(ctx context.Context) (string, error) {
				defer exited.Add(1)
				<-ctx.Done() // 真实阻塞在取消通知上，owner失败之后必须触发并等待这条路径。
				return "", ctx.Err()
			})
		}
		return wantErr
	}()
	if !errors.Is(err, wantErr) || exited.Load() != 2 || report.TasksJoined != 2 {
		t.Fatalf("早退资源不完整：error=%v exited=%d joined=%d", err, exited.Load(), report.TasksJoined)
	}
}

// liveConfig 是实库测试的明确开关。默认go test不加载秘密、不连接服务，并清楚报告SKIP。
// GOLEARN_REDIS_INTEGRATION=1 是执行者本次运行环境，不写用户全局配置。
func liveConfig(t *testing.T) Config {
	t.Helper()
	if os.Getenv("GOLEARN_REDIS_INTEGRATION") != "1" {
		t.Skip("未设置 GOLEARN_REDIS_INTEGRATION=1；本测试需要真实Redis，不能由默认单测代替")
	}
	cfg, err := LoadConfig(DefaultIndex())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestLiveSuite 执行全部真实实验，然后另建正常client独立读取精确键，验证Run确实清理。
// 报告检查只确认覆盖项，业务副作用由各实验独立GET/PTTL断言和这里的外部EXISTS共同证明。
func TestLiveSuite(t *testing.T) {
	cfg := liveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	report, err := Run(ctx, cfg, io.Discard, true)
	if err != nil {
		t.Fatalf("真实实验失败：%v", err)
	}
	if !report.Completed || !report.Cleaned || report.ConnectionsOpen != 0 || report.TasksStarted != report.TasksJoined {
		t.Fatalf("Run资源事实不完整：completed=%t cleaned=%t sockets=%d tasks=%d/%d", report.Completed, report.Cleaned, report.ConnectionsOpen, report.TasksJoined, report.TasksStarted)
	}
	checks := make(map[string]bool)
	for _, check := range report.Checks {
		checks[check.Name] = true
	}
	for _, required := range []string{"actual_expiry", "wrongtype_is_server_error", "json_error_is_not_miss", "incr_repairs_lost_updates", "multi_exec_no_sql_rollback", "lua_error_keeps_prior_writes", "lua_repairs_orphan_counter", "auth_error_distinct", "pool_wait_cancelled", "pool_timeout_distinct", "context-deadline_read_timeout", "no_automatic_write_replay", "registered_key_ttls"} {
		if !checks[required] {
			t.Errorf("未执行关键实验：%s", required)
		}
	}
	stats := &socketStats{}
	client := redis.NewClient(options(cfg, stats))
	defer func() {
		if err := client.Close(); err != nil {
			t.Errorf("独立验收client Close：%v", err)
		}
		if _, open := stats.snapshot(); open != 0 {
			t.Errorf("独立验收连接未关闭=%d", open)
		}
	}()
	seen := make(map[string]bool)
	for _, key := range report.Keys {
		if !strings.HasPrefix(key, report.Prefix) || !strings.HasPrefix(key, "golearn:m25:") || seen[key] {
			t.Fatalf("精确键清单越界或重复：%s", key)
		}
		seen[key] = true
	}
	remaining, err := client.Exists(ctx, report.Keys...).Result()
	if err != nil || remaining != 0 {
		t.Fatalf("另一client独立EXISTS：remaining=%d error=%v", remaining, err)
	}
	t.Logf("真实版本=%s namespace=%s checks=%d keys=%d externally_remaining=%d tasks=%d/%d sockets=%d/%d",
		cfg.ServerVersion, report.Prefix, len(report.Checks), len(report.Keys), remaining,
		report.TasksJoined, report.TasksStarted, report.ConnectionsOpen, report.ConnectionsMade)
}

// TestLiveEarlyFailure 在真实GET响应已扣留时故意使owner失败；由defer取消/释放/join/Close。
// 这项测试直接观察任务退出与真实socket计数，避免只测试正常return后“看起来清理”的情况。
func TestLiveEarlyFailure(t *testing.T) {
	cfg := liveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	stats := &socketStats{}
	var report Report
	l := &lab{cfg: cfg, stats: stats, report: &report, out: io.Discard}
	wantErr := errors.New("deliberate early owner failure after real reply captured")
	err := func() (err error) {
		client, gate, err := l.faultClient(ctx, true, 2*time.Second, time.Second)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, client.Close()) }()
		g := newTaskGroup(ctx, &report)
		g.releases, g.forceClose = []func(){gate.open}, client.Close
		defer g.finish(&err)
		gate.arm()
		worker := g.start(func(workerCtx context.Context) (string, error) {
			return client.Ping(workerCtx).Result() // 真实协议，无键副作用。
		})
		if err := awaitCaptured(ctx, gate, worker); err != nil {
			return err
		}
		return wantErr
	}()
	made, open := stats.snapshot()
	if !errors.Is(err, wantErr) || report.TasksStarted != 1 || report.TasksJoined != 1 || made < 1 || open != 0 {
		t.Fatalf("真实早退资源：error=%v tasks=%d/%d sockets=%d/%d", err, report.TasksJoined, report.TasksStarted, open, made)
	}
	t.Logf("真实响应捕获后的owner早退：all Done joined=%d sockets=%d/%d", report.TasksJoined, open, made)
}

// TestLiveWarmupErrorChain 使faultClient在真实认证阶段失败，检验返回的包装没有丢失RedisError。
// caller拿不到client/gate时，被创建过的真实连接也必须已经Close；不留给caller猜测清理。
func TestLiveWarmupErrorChain(t *testing.T) {
	cfg := liveConfig(t)
	cfg.Password += "-m25-warmup-deliberately-invalid"
	stats := &socketStats{}
	l := &lab{cfg: cfg, stats: stats}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, gate, err := l.faultClient(ctx, true, time.Second, time.Second)
	// HasErrorPrefix内部用errors.As沿Join/%w找到Redis Error接口，而不是匹配我们添加的文案。
	var serverError redis.Error
	made, open := stats.snapshot()
	if client != nil || gate != nil || !errors.As(err, &serverError) || !redis.HasErrorPrefix(err, "WRONGPASS") || made < 1 || open != 0 {
		t.Fatalf("warmup错误链或清理有缺口：typed_redis_error=%t client_returned=%t gate_returned=%t sockets=%d/%d",
			serverError != nil, client != nil, gate != nil, open, made)
	}
	t.Logf("真实warmup AUTH失败：RedisError/WRONGPASS保留，sockets=%d/%d", open, made)
}
