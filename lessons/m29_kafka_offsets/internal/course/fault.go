package course

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// BrokerFault 只在显式mode运行。它不安装/重启服务，仅与唯一安装者用独占目录握手：
// ready.json→安装者停broker并写stopped→真实Produce失败→failure-observed.json→安装者
// 恢复broker并写resumed→同topic读回停机前消息和恢复后消息。每步等待都是有界的。
func BrokerFault(ctx context.Context, h *Harness, l *Ledger, control string) (err error) {
	if control == "" {
		return fmt.Errorf("broker-fault 必须提供本模块 .local 内的独占 -control；先由安装者协调故障窗口")
	}
	base, err := filepath.Abs(".local")
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(control)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("control 必须是当前module .local 下独占子目录")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, name := range []string{"ready.json", "stopped", "failure-observed.json", "resumed"} {
		if _, err = os.Stat(filepath.Join(dir, name)); err == nil {
			return fmt.Errorf("control 含旧标记 %s；使用新的独占目录", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	topic, err := h.Topic(ctx, "broker-fault", 1)
	if err != nil {
		return err
	}
	group := h.Group("broker-fault.consumer")
	before := Event{h.RunID + ".fault.before", "order-persistent", 0}
	beforeACKs, err := h.Produce(ctx, topic, []Event{before}, []int32{0})
	if err != nil {
		return err
	}
	if err = WriteMarker(filepath.Join(dir, "ready.json"), map[string]string{"run_id": h.RunID, "topic": topic, "stage": "pre-stop message broker-acked; safe to stop course Kafka only"}); err != nil {
		return err
	}
	h.Trace.Say("FAULT_READY control=%s topic=%s pre_stop_event=%s", dir, topic, before.ID)
	if err = WaitMarker(ctx, filepath.Join(dir, "stopped")); err != nil {
		return err
	}
	failedCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	unknown := Event{h.RunID + ".fault.unknown", "order-failed", 0}
	failedOwner, err := NewClient(h.Config, h.Trace, h.RunID+".failed-producer", kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		cancel()
		return err
	}
	defer func() { combineCleanup(&err, failedOwner.Close()) }()
	value, err := json.Marshal(unknown)
	if err != nil {
		cancel()
		return err
	}
	failureStart := time.Now() // 耗时仅是本轮观察，不作为broker状态/调度因果证据。
	produceErr := ProduceAndWait(failedCtx, failedOwner, &kgo.Record{Topic: topic, Partition: 0, Key: []byte(unknown.Order), Value: value})
	cancel()
	// 预期的produce失败与意外的Close/join失败分开检查；后者不能伪装成故障实验成功。
	// 只有Close真正返回nil（包含独立done已join），才能许可安装者恢复broker。
	if err = failedOwner.Close(); err != nil {
		return fmt.Errorf("失败producer未完整关闭: %w", err)
	}
	if errors.Is(produceErr, errCleanup) {
		return fmt.Errorf("produce故障包含未通过的资源收尾: %w", produceErr)
	}
	if err = require(produceErr != nil, "broker已停时Produce必须报告失败"); err != nil {
		return err
	}
	// 错误正文来自本实验的合成消息/loopback网络请求，不输出Config或凭据。
	// 保留真正的驱动失败原因，不能只打印error接口动态类型而让读者无法定位错误。
	h.Trace.Say("FAULT_PRODUCE_FAILED error=%q error_type=%T deadline_exceeded=%t client_closed=%t elapsed_ms=%d；未将错误自动解释为业务一定未发生", produceErr.Error(), produceErr, errors.Is(produceErr, context.DeadlineExceeded), errors.Is(produceErr, kgo.ErrClientClosed), time.Since(failureStart).Milliseconds())
	if err = WriteMarker(filepath.Join(dir, "failure-observed.json"), map[string]string{"run_id": h.RunID, "stage": "failed producer returned and ClientOwner joined; safe to restore"}); err != nil {
		return err
	}
	if err = WaitMarker(ctx, filepath.Join(dir, "resumed")); err != nil {
		return err
	}
	after := Event{h.RunID + ".fault.after", "order-resumed", 0}
	afterACKs, err := h.Produce(ctx, topic, []Event{after}, []int32{0})
	if err != nil {
		return err
	}
	// 返回的record指针由同步producer填写过broker offset，借用只读且client已完整关闭。
	// 保留两条ACK的原始字节，恢复消费必须逐字匹配，不能只看稳定event_id就宣称持久值相同。
	acks := map[string]*kgo.Record{before.ID: beforeACKs[0], after.ID: afterACKs[0]}
	return h.WithConsumer(ctx, topic, group, func(cl *kgo.Client) error {
		defer cl.AllowRebalance()
		seen := make(map[string]bool)
		for !seen[before.ID] || !seen[after.ID] {
			rs, e := ReadN(ctx, cl, 1, h.Trace)
			if e != nil {
				return e
			}
			r := rs[0]
			event, e := Decode(r)
			if e != nil {
				return e
			}
			if ack := acks[event.ID]; ack != nil {
				if e = require(bytes.Equal(ack.Key, r.Key) && bytes.Equal(ack.Value, r.Value) && ack.Partition == r.Partition && ack.Offset == r.Offset, "恢复消息必须与停机前/恢复后ACK的原始key/value/partition/offset一致"); e != nil {
					return e
				}
			} else if e = require(event.ID == unknown.ID && event.Order == unknown.Order && event.Step == unknown.Step, "恢复topic只能包含本run两条ACK消息和结果未知的尝试"); e != nil {
				return e
			}
			seen[event.ID] = true
			if e = ApplyRecord(ctx, l, r, "broker-fault", "recovered", true); e != nil {
				return e
			}
			if e = h.Commit(ctx, cl, group, r); e != nil {
				return e
			}
		}
		h.Trace.Say("BROKER_FAULT_PASS pre_stop_value_persisted=true recovered_produce_readback=true unknown_event_observed=%t", seen[unknown.ID])
		return nil
	})
}

// WriteMarker 由故障实验owner调用。O_EXCL 拒绝覆盖旧握手；Write/Sync/Close错误聚合，
// 不把成功打印ready日志代替实际磁盘标记，安装者只消费这个run的无秘密文件。
func WriteMarker(path string, value any) (err error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { combineCleanup(&err, f.Close()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// WaitMarker 的ticker只用于重复读取明确文件状态，不用固定等待时间推测broker已停。
// 每次Stat都检查实际标记；ctx到期退出，defer Stop释放本函数自己创建的ticker。
func WaitMarker(ctx context.Context, path string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("等待安装者标记 %s: %w", filepath.Base(path), ctx.Err())
		}
	}
}
