//go:build faultcases

// Package faults 只在显式 -tags=faultcases 时编译。
// 所有函数都故意错误，不供正常业务调用；cmd/probe 用有截止时间的子进程隔离 fatal/race。
package faults

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
)

// Run 由故障 main 同步调用。它仅选择一个场景，每个场景的数据与进程都独立。
// 返回 nil 不代表坏代码正确：race 场景可能返回，但 -race 会报告且用约定退出码终止进程。
func Run(mode string) error {
	fmt.Println("FAULT begin:", mode)
	runtime.GOMAXPROCS(4)
	switch mode {
	case "map":
		ordinaryMap()
	case "different-lock":
		differentLocks()
	case "value-receiver":
		copiedReceiver()
	case "copy-struct":
		copiedUnlockedStruct()
	case "copy-locked":
		copiedLockedStruct()
	case "reentry":
		reentry()
	case "rw-upgrade":
		upgrade()
	case "lock-order":
		circularOrder()
	case "atomic-value-map":
		valueInternalMap()
	case "slice":
		backingArray()
	case "visibility":
		unsynchronizedPublication()
	default:
		return fmt.Errorf("未知故障模式 %q", mode)
	}
	return nil
}

// pair 由 race 实验调用，owner 创建恰好两个 worker，并把 start 当同步开始门。
// close(start) 只使两者都可运行，没有建立两者后续读写之间的 happens-before。
// 所有有限循环结束后，owner 用 Wait 等退出；Wait 不能修复循环期间已发生的冲突。
func pair(left, right func()) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() { <-start; left() })
	wg.Go(func() { <-start; right() })
	close(start)
	wg.Wait()
}

// ordinaryMap 展示普通 map 的读写与写写冲突；不同 key 写入也不能绕开内部共享结构。
// 循环有上限。非 -race 子进程验证本次 runtime fatal，-race 子进程验证具体冲突栈。
func ordinaryMap() {
	shared := make(map[int]int)
	pair(func() {
		for n := range 100000 {
			shared[n%16] = n
		}
	}, func() {
		for n := range 100000 {
			shared[(n+1)%16] = n
			_ = shared[n%16]
		}
	})
}

// differentLocks 由单独子进程执行。同一个 count 地址被两把互不相关的锁“保护”。
// 只有对同一把 mu 的 Unlock→Lock 才建立需要的同步边界；锁住局部对象不保护别人的访问。
func differentLocks() {
	count := 0
	var leftMu, rightMu sync.Mutex
	pair(func() {
		for range 5000 {
			leftMu.Lock()
			count++
			leftMu.Unlock()
		}
	}, func() {
		for range 5000 {
			rightMu.Lock()
			count++
			rightMu.Unlock()
		}
	})
}

// copiedCounter 的 count 是共享指针，mu 却在赋值/值接收者时随 struct 一起复制。
// 表面上每次 add 都加锁，实际两个调用使用自己的锁副本并改同一个 *count。
type copiedCounter struct {
	mu    sync.Mutex
	count *int
}

// add 是故意错误的值接收者，进入函数前复制整个 copiedCounter。
// go vet 的 copylocks 检查直接指出这一签名；修复见正常包 Inventory 的指针接收者。
func (c copiedCounter) add() {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.count++
}

// copiedReceiver 使用同一个 source 调用值接收者，复现看似“对象方法内部锁了”的 race。
func copiedReceiver() {
	count := 0
	source := copiedCounter{count: &count}
	pair(func() {
		for range 5000 {
			source.add()
		}
	}, func() {
		for range 5000 {
			source.add()
		}
	})
}

// copiedUnlockedStruct 先使用 source.mu 再复制结构，直接违反“首次使用后不得复制”的契约。
// 即使复制时锁当前未加锁，也不能据此允许复制：这里 count 别名仍共享，锁状态却分离。
func copiedUnlockedStruct() {
	count := 0
	source := copiedCounter{count: &count}
	source.mu.Lock()
	source.mu.Unlock()
	copyOfSource := source
	pair(func() {
		for range 5000 {
			source.mu.Lock()
			*source.count++
			source.mu.Unlock()
		}
	}, func() {
		for range 5000 {
			copyOfSource.mu.Lock()
			*copyOfSource.count++
			copyOfSource.mu.Unlock()
		}
	})
}

// copiedLockedStruct 复制的 mu 带着“已加锁”状态。原锁的 Unlock 不能解开这个副本。
// 主 goroutine 在副本上 Lock 永久等待；defer source.Unlock 因函数未退出而不能执行。
func copiedLockedStruct() {
	var source copiedCounter
	source.mu.Lock()
	defer source.mu.Unlock()
	copyOfSource := source
	copyOfSource.mu.Lock()
}

// reentry 演示 Mutex 不支持 Java synchronized/ReentrantLock 那样的重入。
// 同一个 goroutine 第二次 Lock 也等待释放，不会记忆“当前 owner 是我”。
func reentry() {
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
	mu.Lock()
}

// upgrade 演示持 RLock 后再 Lock 不会升级；写锁要等包括自己的所有 reader 释放。
// 当前 goroutine 却只有 Lock 返回后才会执行 RUnlock，形成无法完成的等待。
func upgrade() {
	var mu sync.RWMutex
	mu.RLock()
	defer mu.RUnlock()
	mu.Lock()
}

// circularOrder 用屏障固定两者“各自持有第一把锁”，不靠 sleep 猜测死锁窗口。
// owner 关闭 secondGate 后，两者分别等待对方的锁；owner 在 Wait 等两者，全部无法推进。
func circularOrder() {
	var a, b sync.Mutex
	held := make(chan struct{}, 2)
	secondGate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		a.Lock()
		defer a.Unlock()
		held <- struct{}{}
		<-secondGate
		b.Lock()
		defer b.Unlock()
	})
	wg.Go(func() {
		b.Lock()
		defer b.Unlock()
		held <- struct{}{}
		<-secondGate
		a.Lock()
		defer a.Unlock()
	})
	<-held
	<-held
	close(secondGate)
	wg.Wait()
}

// valueInternalMap 发布后原地写 map 的两个引用，都来自安全的 Value.Load。
// 原子性只覆盖 Value 里“保存哪个值”，不覆盖值指向的 map 内容；修复见 Snapshots。
func valueInternalMap() {
	var value atomic.Value
	value.Store(map[string]int{"sku-1": 1})
	pair(func() {
		m := value.Load().(map[string]int)
		for range 5000 {
			m["sku-1"]++
		}
	}, func() {
		m := value.Load().(map[string]int)
		sum := 0
		for range 5000 {
			sum += m["sku-1"]
		}
		fmt.Println("错误的 map 读者观察 sum:", sum)
	})
}

// backingArray 的 left/right 是两个 slice 头，却指向同一元素；变量名不同不等于对象独立。
// 只在共享数组上读写，切片长度/容量不变化，便于从报告定位到真正冲突的元素。
func backingArray() {
	left := []int{1, 2}
	right := left[:]
	pair(func() {
		for range 5000 {
			left[0]++
		}
	}, func() {
		sum := 0
		for range 5000 {
			sum += right[0]
		}
		fmt.Println("错误的 slice 读者观察 sum:", sum)
	})
}

// unsynchronizedPublication 反例不用永久忙等：读者检查次数固定，owner 最后 Wait 收尾。
// 但写者的普通 ready=true 与读者普通 if ready 没有同步契约；恰好读到 true 也不建立发布。
func unsynchronizedPublication() {
	var ready bool
	var payload string
	pair(func() {
		for range 5000 {
			payload = "完整配置"
			ready = true
		}
	}, func() {
		last := ""
		for range 5000 {
			if ready {
				last = payload
			}
		}
		fmt.Println("无同步发布观察:", last)
	})
}
