// demo 的 main 是实验 owner：解析输入、限制运行时间、拥有子进程和 profile 文件。
// 正常入口不会制造 OOM、启动常驻服务器或修改任何共享服务资源设置。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"time"

	"go-demo/lessons/m35_performance_runtime/internal/lab"
)

// options 是本次进程的输入值，子进程通过显式参数取得自己的 variant 和预算。
type options struct {
	mode, variant, profileDir string
	gogc, batch               int
	limit                     int64
	duration                  time.Duration
}

// main 不在 goroutine 中 recover 掩盖失败。run 把资源错误交回来，打印并设置非零退出码。
// defer 清理放在 run 及工作函数里，避免 os.Exit 跳过仍需执行的清理。
func main() {
	var opt options
	flag.StringVar(&opt.mode, "mode", "all", "all/core/retention/gc/cpu/mutex")
	flag.StringVar(&opt.variant, "variant", "alias", "retention=alias/cap/clone; cpu=fmt/append")
	flag.StringVar(&opt.profileDir, "profile-dir", "", "可选 profile 目录；建议 .local/profiles")
	flag.IntVar(&opt.gogc, "gogc", 100, "GC 子进程 GOGC")
	flag.IntVar(&opt.batch, "batch", 1, "mutex 每次加锁处理多少条")
	flag.Int64Var(&opt.limit, "limit", math.MaxInt64, "GC 子进程软内存预算（字节）")
	flag.DurationVar(&opt.duration, "duration", 2*time.Second, "CPU profile 运行 0.2s..5s")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := run(ctx, opt); err != nil {
		fmt.Fprintln(os.Stderr, "M35 实验失败:", err)
		os.Exit(1)
	}
}

// run 按模式分派。子进程模式仅输出一条 JSON，父进程才能严格解码而非猜日志成功。
// profile 文件在工作目录下显式目录创建；本课程复跑时使用模块内 .local/profiles。
func run(ctx context.Context, opt options) error {
	if opt.profileDir != "" {
		if err := os.MkdirAll(opt.profileDir, 0o755); err != nil {
			return fmt.Errorf("创建 profile 目录: %w", err)
		}
	}
	switch opt.mode {
	case "all", "core":
		fmt.Printf("E0 build=%s %s/%s NumCPU=%d GOMAXPROCS=%d env.GOMAXPROCS=%q\n",
			runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), os.Getenv("GOMAXPROCS"))
		fmt.Println("E0", lab.AffinityDescription())
		if err := core(ctx); err != nil {
			return err
		}
		if opt.mode == "all" {
			return isolated(ctx, opt.profileDir)
		}
		return nil
	case "retention":
		// 采样率 1 表示尽量记录全部分配，不是生产默认设置；仅此隔离进程使用。
		runtime.MemProfileRate = 1
		result, err := lab.RetentionWork(opt.variant, func() error {
			return writeProfile(opt.profileDir, "heap-"+opt.variant+".pprof", "heap")
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "gc":
		result, err := lab.GCWork(opt.gogc, opt.limit)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "cpu":
		return cpuWork(ctx, opt)
	case "mutex":
		// mutex/block 采样增加成本，只用于定位。这次 wall time 不用来承诺速度收益。
		oldRate := runtime.SetMutexProfileFraction(1)
		defer runtime.SetMutexProfileFraction(oldRate)
		runtime.SetBlockProfileRate(1)
		defer runtime.SetBlockProfileRate(0)
		result, err := lab.CounterDemo(ctx, opt.batch)
		if err != nil {
			return err
		}
		if result.Total != 200_000 {
			return fmt.Errorf("订单计数不守恒: %+v", result)
		}
		for _, name := range []string{"mutex", "block"} {
			if err := writeProfile(opt.profileDir, name+"-batch"+strconv.Itoa(opt.batch)+".pprof", name); err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	default:
		return fmt.Errorf("未知 mode %q", opt.mode)
	}
}

// core 是 10..20 分钟阅读路线中的语义入口：先业务结果，再所有权、栈和锁总数。
// 所有错误都返回 run；断言发生在实际计算完成后，不以“进程返回 0”代替业务事实。
func core(ctx context.Context) error {
	order := lab.Order{ShopID: 42, OrderID: 9001}
	baseline, changed := lab.KeyFmt(order), lab.KeyAppend(order)
	if baseline != "shop:42/order:9001" || changed != baseline {
		return fmt.Errorf("订单键协议错误: %q / %q", baseline, changed)
	}
	fmt.Printf("E1 key fmt=%q append=%q invariant=PASS\n", baseline, changed)
	body := make([]byte, 64)
	body[0] = 'A'
	alias, capTrap, clone := lab.HeaderAlias(body), lab.HeaderCapTrap(body), lab.HeaderClone(body)
	body[0] = 'B'
	if alias[0] != 'B' || capTrap[0] != 'B' || clone[0] != 'A' {
		return fmt.Errorf("header 所有权不符合预期")
	}
	fmt.Printf("E2 source=B alias=%c cap_trap=%c clone=%c cap=%d invariant=PASS\n", alias[0], capTrap[0], clone[0], cap(capTrap))
	scratch := &lab.Scratch{Data: []byte("secret-order")}
	borrowed, owned := scratch.Data, string(scratch.Data)
	scratch.ResetForReuse()
	if borrowed[0] != 0 || owned != "secret-order" {
		return fmt.Errorf("缓冲独立复制或清零失败")
	}
	fmt.Printf("E3 borrowed_after_reset=%d owned=%q pool_key=%q invariant=PASS\n", borrowed[0], owned, lab.NewKeyPool().Key(order))
	stack, err := lab.StackDemo(ctx)
	if err != nil {
		return err
	}
	if stack.Checksum != 4*65*65 {
		return fmt.Errorf("栈递归工作被错误修改: %+v", stack)
	}
	fmt.Printf("E4 stack before=%d deep=%d after=%d checksum=%d invariant=PASS\n", stack.Before, stack.Deep, stack.After, stack.Checksum)
	for _, batch := range []int{1, 100} {
		counter, err := lab.CounterDemo(ctx, batch)
		if err != nil {
			return err
		}
		if counter.Total != 200_000 || counter.LockCalls != 200_000/batch {
			return fmt.Errorf("计数或锁次数错误: %+v", counter)
		}
		fmt.Printf("E5 batch=%d total=%d lock_calls=%d observation=%s invariant=PASS\n", batch, counter.Total, counter.LockCalls, counter.Elapsed)
	}
	return nil
}

// child 由 all 入口同步调用同一个已编译可执行文件，不再启动 go build。
// parent 给每个子进程 25 秒，Output 等待完成并回收；失败时保留退出原因及 stderr。
// 参数逐项传给进程，不拼接 shell 命令，也不更改调用方环境里的 GOGC/GOMEMLIMIT。
func child(ctx context.Context, args []string, result any) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	childCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	output, err := exec.CommandContext(childCtx, executable, args...).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("子进程 %v: %w stderr=%s", args, err, exit.Stderr)
		}
		return fmt.Errorf("子进程 %v: %w", args, err)
	}
	if err := json.Unmarshal(output, result); err != nil {
		return fmt.Errorf("子进程 JSON 无效: %w output=%q", err, output)
	}
	return nil
}

// isolated 按顺序执行，任意时刻只有一个内存实验子进程。
// variant 对照不共用堆，所以前一个实验留下的 span/GC 参数不能污染后一个。
func isolated(ctx context.Context, profiles string) error {
	retained := make(map[string]lab.RetentionResult)
	for _, variant := range []string{"alias", "cap", "clone"} {
		var result lab.RetentionResult
		if err := child(ctx, []string{"-mode", "retention", "-variant", variant, "-profile-dir", profiles}, &result); err != nil {
			return err
		}
		if result.LogicalBytes != 128 || result.Checksum != 576 {
			return fmt.Errorf("保留 header 的业务内容不正确: %+v", result)
		}
		retained[variant] = result
		fmt.Printf("E6 %s logical=%d heap=%d runtime=%d working_set=%d os_error=%q checksum=%d\n",
			variant, result.LogicalBytes, result.After.HeapAlloc, result.After.RuntimeBytes, result.After.WorkingSet, result.After.WorkingSetError, result.Checksum)
	}
	// 这项只作为本有界程序的实测验收：8*4MiB 可达数组应产生明显的 heap 差距。
	// 它不指定 Go 所有对象的大小，也不把 clone 总堆占用断言为精确 128 字节。
	for _, variant := range []string{"alias", "cap"} {
		if retained[variant].After.HeapAlloc <= retained["clone"].After.HeapAlloc+16*lab.MiB {
			return fmt.Errorf("32MiB 可达大数组的保留证据不足: %s vs clone", variant)
		}
	}
	fmt.Println("E6 retention_content_and_heap_gap=PASS (cap 限制没有释放大数组)")
	for _, config := range []struct {
		gogc  int
		limit int64
	}{{50, math.MaxInt64}, {200, math.MaxInt64}, {100, int64(8 * lab.MiB)}} {
		var result lab.GCResult
		if err := child(ctx, []string{"-mode", "gc", "-gogc", strconv.Itoa(config.gogc), "-limit", strconv.FormatInt(config.limit, 10)}, &result); err != nil {
			return err
		}
		if result.LiveBytes != 12*lab.MiB || result.ChurnBytes != 96*lab.MiB || result.Checksum != 199_680 || result.WorkTotalAlloc < result.ChurnBytes {
			return fmt.Errorf("GC 工作量或计算内容无效: %+v", result)
		}
		if config.limit == int64(8*lab.MiB) && result.After.RuntimeBytes <= uint64(config.limit) {
			return fmt.Errorf("live 已达 12MiB，本次未观察到 8MiB 软限制可超出")
		}
		fmt.Printf("E7 GOGC=%d limit=%d live=%d churn=%d work_gc=%d allocated=%d work_ms=%.2f pause_ms=%.2f heap=%d runtime=%d working_set=%d os_error=%q checksum=%d\n",
			result.GOGC, result.Limit, result.LiveBytes, result.ChurnBytes, result.WorkNumGC, result.WorkTotalAlloc,
			float64(result.WorkElapsedNS)/1e6, float64(result.PauseNS)/1e6, result.After.HeapAlloc,
			result.After.RuntimeBytes, result.After.WorkingSet, result.After.WorkingSetError, result.Checksum)
	}
	fmt.Println("E7 workload_content_and_soft_limit=PASS (GC 次数/耗时关系仅作本次观察)")
	return nil
}

// writeProfile 同步写出已启用的 runtime profile，自己拥有并关闭文件。
// Heap profile 在 RetentionWork 的 keep 存活阶段调用；mutex/block 在 worker 已退出后写。
func writeProfile(dir, filename, kind string) error {
	if dir == "" {
		return nil
	}
	profile := pprof.Lookup(kind)
	if profile == nil {
		return fmt.Errorf("没有 %s profile", kind)
	}
	file, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return err
	}
	writeErr := profile.WriteTo(file, 0)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// cpuWork 仅为定位运行 0.2..5 秒，不从 profile 中宣称准确的 ns/op。
// 每批 4096 次后检查 deadline/ctx，把 time.Now 的检查成本压到批次边界。
// CPU profiler 由本函数拥有，StopCPUProfile 先完成写出，再关闭底层文件。
func cpuWork(ctx context.Context, opt options) error {
	if opt.profileDir == "" || (opt.variant != "fmt" && opt.variant != "append") || opt.duration < 200*time.Millisecond || opt.duration > 5*time.Second {
		return fmt.Errorf("cpu 要求 profile-dir、variant=fmt/append、duration=0.2s..5s")
	}
	file, err := os.Create(filepath.Join(opt.profileDir, "cpu-"+opt.variant+".pprof"))
	if err != nil {
		return err
	}
	if err := pprof.StartCPUProfile(file); err != nil {
		_ = file.Close()
		return err
	}
	defer file.Close()
	defer pprof.StopCPUProfile() // 后注册先执行，确保 Stop 写完后才 Close。
	order := lab.Order{ShopID: 42, OrderID: 9001}
	var operations, checksum uint64
	deadline := time.Now().Add(opt.duration)
	for time.Now().Before(deadline) && operations < 100_000_000 {
		if err := ctx.Err(); err != nil {
			return err
		}
		for range 4096 {
			order.OrderID++
			var key string
			if opt.variant == "fmt" {
				key = lab.KeyFmt(order)
			} else {
				key = lab.KeyAppend(order)
			}
			// 消费生成字符串：循环对程序结果有作用，不能整段删掉。
			checksum += uint64(len(key)) + uint64(key[len(key)-1])
			operations++
		}
	}
	fmt.Printf("E8 CPU variant=%s operations=%d checksum=%d profile=%s\n", opt.variant, operations, checksum, file.Name())
	return nil
}
