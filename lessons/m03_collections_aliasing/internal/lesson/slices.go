package lesson

import (
	"fmt"
	"runtime"
	"slices"
)

// mutateAndAppend 由 slicesExperiment 调用。参数 s 接收描述值副本，不是调用者变量的地址。
// 返回值把新的 len/cap 交回调用者；是否使用返回值，是调用者的明确决定。
func mutateAndAppend(s []string) []string {
	s[0] = "A*"            // 元素写入命中共享数组；即使不接返回值，调用方仍看得到。
	s = append(s, "LOCAL") // 此处有空余容量：写入数组下标 2，同时只改变局部 s 的长度。
	return s
}

// slicesExperiment 由 Run 同步调用，数组由本函数创建并在函数退出后不再被本实验持有。
// 所有容量都由 make/完整切片表达式确定，结论不依赖 runtime 的具体扩容倍率。
func slicesExperiment(t *transcript) error {
	t.log("\n[E01] 描述值传参、两次 append 与复制边界")
	caller := make([]string, 2, 4)
	copy(caller, []string{"A", "B"})
	returned := mutateAndAppend(caller)
	t.log("传参: caller=%v len=%d cap=%d; returned=%v len=%d; 隐藏槽=%v", caller, len(caller), cap(caller), returned, len(returned), caller[:cap(caller)])
	if err := require(len(caller) == 2 && caller[0] == "A*" && caller[:4][2] == "LOCAL", "E01: 元素写入应共享，但参数 append 不应改 caller 的 len"); err != nil {
		return err
	}

	base := []string{"A", "B", "C", "D"}
	prefix := base[:2] // len=2 cap=4；并未复制 A/B。
	first := append(prefix, "X")
	// prefix 自己仍为 len=2，第二次追加再次写入下标 2，覆盖 first 的第三项。
	second := append(prefix, "Y")
	t.log("有容量两次 append: base=%v first=%v second=%v; 同一第0项=%v", base, first, second, &base[0] == &first[0])
	if err := require(first[2] == "Y" && base[2] == "Y", "E01: 第二次 append 应覆盖第一份结果的第三项"); err != nil {
		return err
	}

	full := []string{"A", "B", "C", "D"}
	limited := full[:2:2] // [low:high:max] 给出 len=2、cap=2；容量从起点 low 计算。
	isolatedFirst := append(limited, "X")
	isolatedSecond := append(limited, "Y")
	t.log("无空余容量两次 append: base=%v first=%v second=%v; 同一第0项=%v", full, isolatedFirst, isolatedSecond, &full[0] == &isolatedFirst[0])
	if err := require(full[2] == "C" && isolatedFirst[2] == "X" && isolatedSecond[2] == "Y", "E01: 越过 cap 的两次 append 应分别分配数组"); err != nil {
		return err
	}
	// 三下标切片只限制未来 append，现有元素依然共享，不能把 limited 当独立快照。
	limited[0] = "still shared"
	t.log("容量封顶后直接改元素: full[0]=%q，已扩容结果 first[0]=%q", full[0], isolatedFirst[0])

	byCopy := make([]string, len(base))
	n := copy(byCopy, base) // 只复制 min(len(dst), len(src)) 项，不会帮 dst 扩长。
	byClone := slices.Clone(base)
	byCopy[0], byClone[1] = "COPY", "CLONE"
	t.log("copy/Clone: n=%d source=%v copied=%v cloned=%v", n, base, byCopy, byClone)
	short := make([]string, 1)
	t.log("copy 到 len=1 的目标: n=%d，目标=%v", copy(short, base), short)
	// 空结果也验证：不能用 &s[0] 判断空切片的共享关系，因为这个下标不存在。
	if err := require(base[0] == "A" && base[1] == "B" && n == 4, "E01: []string 的复制应独立且 copy 返回真实数量"); err != nil {
		return err
	}
	t.log("E01 PASS（只断言共享/分离和长度，不断言扩容后的具体 cap）")
	return nil
}

// retentionExperiment 由 cache 实验调用，用有界 1 MiB 数据展示“小窗口仍引用大数组”。
// 不采样 HeapAlloc，不靠 GC 时机判定释放；地址比较和修改观察证明具体别名，资源结局另说明。
func retentionExperiment(t *transcript) error {
	large := make([]byte, 1<<20)
	copy(large, []byte("ABCD"))
	window := large[:4:4] // cap=4 也仍来自那个 1 MiB 数组；封顶容量不是释放数组。
	compact := make([]byte, len(window))
	copy(compact, window) // 新分配四字节数组，切断长期缓存对旧数组的引用。
	shared := &window[0] == &large[0]
	independent := &compact[0] != &large[0]
	large[0] = 'Z'
	t.log("E04 小窗口: 原数组=%d bytes window len/cap=%d/%d shared=%v 值=%q; copied independent=%v 值=%q", len(large), len(window), cap(window), shared, window, independent, compact)
	if err := require(shared && independent && string(window) == "ZBCD" && string(compact) == "ABCD", "E04: 封顶窗口仍共享，copy 必须切断数组别名"); err != nil {
		return err
	}
	// 清除本函数对大数组的两条引用。若业务缓存只留 compact，就不会通过它保留旧数组。
	// “现在可回收”与“GC 已回收/进程 RSS 已下降”不同，本实验不声称后两项。
	large, window = nil, nil
	runtime.KeepAlive(compact) // 保证观察期间四字节结果仍活跃；不是要求 GC 立刻执行。

	// 再让创建者真的返回，把小前缀保存在缓存中。此时工厂内的 large 局部变量已离开作用域，
	// 缓存里的 slice 仍引用整块数组；主动 GC 后，容量范围内的末尾哨兵依然必须可读取。
	prefixCache := map[string][]byte{"header": retainedPrefix()}
	stored := prefixCache["header"]
	runtime.GC() // 只让一次 GC 完成，不用它推断回收量或进程 RSS。
	last := stored[:cap(stored)][cap(stored)-1]
	t.log("E04 生命周期: 工厂已返回，缓存 len/cap=%d/%d，GC后数组末尾哨兵=%d", len(stored), cap(stored), last)
	if err := require(len(stored) == 4 && cap(stored) == 1<<20 && last == 7, "E04: 创建者返回和 GC 不应破坏缓存仍持有的数组"); err != nil {
		return err
	}
	independentCache := make([]byte, len(stored))
	copy(independentCache, stored)
	prefixCache["header"] = independentCache // 缓存替换为独立小数组，移除它对大数组的引用。
	stored = nil                             // 调用者自己也放弃旧窗口；只改缓存条目而保留 stored，旧数组仍被引用。
	t.log("E04 生命周期修复: 缓存 len/cap=%d/%d 值=%q，旧窗口引用已清除", len(prefixCache["header"]), cap(prefixCache["header"]), prefixCache["header"])
	t.log("E04 PASS：large/window/stored 的本地引用已清除；未测量 GC/RSS")
	return nil
}

// retainedPrefix 由 retentionExperiment 调用，模拟大报文解析器只返回短请求头。
// 返回时没有复制四个字节，底层 1 MiB 数组通过返回的 slice 继续活着；由调用者缓存保存它。
// 末尾哨兵只用于证明工厂返回/GC 后容量范围里的存储仍存在，不属于请求头业务内容。
func retainedPrefix() []byte {
	large := make([]byte, 1<<20)
	copy(large, []byte("ABCD"))
	large[len(large)-1] = 7
	return large[:4]
}

// require 由实验里的业务不变量调用。false 时创建普通 error，让 Run/main 决定失败退出。
// 它不是为了检测偶然的内存地址数字或 map 遍历顺序，只检查业务上必须成立的状态。
func require(ok bool, message string) error {
	if !ok {
		return fmt.Errorf("断言失败: %s", message)
	}
	return nil
}
