package lab

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestKeyContract 用独立的 strconv.FormatInt 定义订单键协议，而非只让两种实现互比。
// 覆盖零、负数和 int64 边界，防止局部数组容量或十进制符号处理错了却仍“更快”。
func TestKeyContract(t *testing.T) {
	pool := NewKeyPool()
	orders := []Order{{0, 0}, {42, 9001}, {-7, 17}, {math.MinInt64, math.MaxInt64}}
	for i := int64(0); i < 500; i++ {
		orders = append(orders, Order{i*7919 - 500_000, i*104729 - 100_000})
	}
	for _, order := range orders {
		want := "shop:" + strconv.FormatInt(order.ShopID, 10) + "/order:" + strconv.FormatInt(order.OrderID, 10)
		for name, key := range map[string]string{"fmt": KeyFmt(order), "append": KeyAppend(order), "pool": pool.Key(order)} {
			if key != want {
				t.Fatalf("%s 输入=%+v 得到=%q 协议要求=%q", name, order, key, want)
			}
		}
	}
}

// TestRetentionOwnership 验证数据所有权，而不把 GC 时刻/字节统计写成语言断言。
// full slice 的 cap 已经变小，但原 body 修改仍能穿透；Clone 的内容保持原样。
func TestRetentionOwnership(t *testing.T) {
	body := bytes.Repeat([]byte{'A'}, 64)
	alias, capTrap, copy := HeaderAlias(body), HeaderCapTrap(body), HeaderClone(body)
	body[0] = 'B'
	if alias[0] != 'B' || capTrap[0] != 'B' || copy[0] != 'A' || cap(capTrap) != 16 {
		t.Fatal("别名应看到 B，Clone 应仍为 A，限制 cap 不应复制数据")
	}
}

// TestBufferOwnership 直接验证危险别名与清零，再验证 Pool 返回字符串的独立性。
// 不断言 Get 返回同一指针：sync.Pool 允许丢弃值，这不是可靠缓存的命中测试。
func TestBufferOwnership(t *testing.T) {
	s := &Scratch{Data: make([]byte, 0, 64)}
	s.Data = append(s.Data, "secret-order"...)
	borrowed := s.Data
	owned := string(s.Data)
	s.ResetForReuse()
	for _, b := range borrowed {
		if b != 0 {
			t.Fatal("归还前曾写入的字节应已清零")
		}
	}
	if owned != "secret-order" || len(s.Data) != 0 {
		t.Fatal("独立字符串应保持内容，缓冲逻辑长度应恢复为零")
	}
	p := NewKeyPool()
	first := p.Key(Order{42, 9001})
	for i := int64(0); i < 100; i++ {
		_ = p.Key(Order{i, i + 1})
	}
	if first != "shop:42/order:9001" {
		t.Fatal("后续池复用不能修改已返回的字符串")
	}
}

// TestPoolConcurrentRequests 让四个请求执行者共享同一个 KeyPool，
// 每个调用独占 Get 到的临时缓冲；返回字符串必须保持本请求自己的订单键。
// test 是 goroutine owner：先 Add，最后 Wait，再关闭 errors；错误 channel 容量为 4，
// 每个 worker 最多发送一项，所以 owner 先等待退出不会阻塞发送者。
func TestPoolConcurrentRequests(t *testing.T) {
	p := NewKeyPool()
	errors := make(chan error, 4)
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := int64(0); i < 100; i++ {
				order := Order{int64(id), i + 9001}
				key := p.Key(order)
				want := "shop:" + strconv.FormatInt(order.ShopID, 10) + "/order:" + strconv.FormatInt(order.OrderID, 10)
				if key != want {
					errors <- fmt.Errorf("worker=%d key=%q want=%q", id, key, want)
					return
				}
			}
		}(worker) // 参数是当前 worker 数字副本，不依赖闭包后来读取循环变量。
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

// TestEscapeSemantics 证明局部/保留路径的值正确。分配数量是版本相关实测，
// 放在 benchmark 和 -m 输出中解释，不把当前优化决定提升为未来版本必须满足的契约。
func TestEscapeSemantics(t *testing.T) {
	if PointerLocal(1024) != 1025 || BoxLocal(1024) != 1025 || ClosureLocal(1024) != 1025 {
		t.Fatal("局部路径都应在当前调用算出 n+1")
	}
	p, box, f := PointerRetained(1024), BoxRetained(1024), ClosureRetained(1024)
	if p.Cents != 1025 || box.(int64) != 1024 || f() != 1025 {
		t.Fatal("函数返回后，保留的地址、动态值和闭包参数应仍有效")
	}
}

// TestWorkersComplete 用受控工作量与 ctx 上限验证资源收尾和业务计数。
// 不判断哪个 worker 先运行，也不要求 StackInuse 返回某个精确值。
func TestWorkersComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stack, err := StackDemo(ctx)
	if err != nil || stack.Checksum != 4*65*65 {
		t.Fatalf("递归帧的 0..64 两端字节和应为 4*65²；result=%+v err=%v", stack, err)
	}
	for _, batch := range []int{1, 100} {
		result, err := CounterDemo(ctx, batch)
		if err != nil || result.Total != 200_000 || result.LockCalls != 200_000/batch {
			t.Fatalf("每个订单应只计一次，锁批量数应吻合；batch=%d result=%+v err=%v", batch, result, err)
		}
	}
	stopped, stop := context.WithCancel(context.Background())
	stop()
	if _, err := CounterDemo(stopped, 1); err == nil {
		t.Fatal("已取消的 owner 应收到取消错误，并已等待全部 worker 退出")
	}
	if _, err := CounterDemo(ctx, 0); err == nil {
		t.Fatal("非法 batch 应在创建 worker 前拒绝")
	}
}
