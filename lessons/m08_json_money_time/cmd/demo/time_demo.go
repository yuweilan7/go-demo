package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go-demo/lessons/m08_json_money_time/internal/contract"
)

// timeDemo 由 run 调用。到期判断使用固定业务时刻；单调钟实验单独取 time.Now。
// 本实验不改系统时间、不 sleep、不建 timer，因此没有等待和残留资源。
func timeDemo() error {
	fmt.Println("\n[E03-A] 同一时刻的 UTC、上海、纽约")
	deadline, err := contract.ParseInstant("2026-10-12T10:00:00+08:00")
	if err != nil {
		return err
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	fmt.Printf("expires UTC=%s 上海=%s 纽约=%s\n", deadline.Format(time.RFC3339), deadline.In(shanghai).Format(time.RFC3339), deadline.In(newYork).Format(time.RFC3339))
	for _, delta := range []time.Duration{-time.Second, 0, time.Second} {
		now := deadline.Add(delta)
		fmt.Printf("相对deadline=%s now上海=%s now纽约=%s expiredUTC=%t expired上海=%t expired纽约=%t\n", delta, now.In(shanghai).Format(time.RFC3339), now.In(newYork).Format(time.RFC3339), contract.Expired(now, deadline.Time), contract.Expired(now.In(shanghai), deadline.Time), contract.Expired(now.In(newYork), deadline.Time))
	}

	fmt.Println("\n[E03-B] 无时区本地文本：Parse 与 ParseInLocation")
	text := "2026-10-12 10:00:00"
	layout := "2006-01-02 15:04:05"        // Go 使用参考时间，不使用 Java 的 yyyy-MM-dd 模式字母。
	wrong, err := time.Parse(layout, text) // 没有时区信息时，Parse 将本地数字解释成 UTC。
	if err != nil {
		return err
	}
	correct, err := time.ParseInLocation(layout, text, shanghai)
	if err != nil {
		return err
	}
	now := deadline.Time
	fmt.Printf("Parse=%s ParseInLocation转UTC=%s 相差=%s；到期wrong=%t correct=%t\n", wrong.Format(time.RFC3339), correct.UTC().Format(time.RFC3339), wrong.Sub(correct), contract.Expired(now, wrong), contract.Expired(now, correct))
	_, rejected := contract.ParseInstant(text)
	fmt.Printf("新接口直接拒绝无offset文本=%v\n", rejected)

	fmt.Println("\n[E03-C] Duration 单位和 Unix 秒/毫秒")
	wrongTTL := time.Duration(30) // Duration 的底层是 int64 纳秒；这表示30ns而不是30分钟。
	correctTTL := 30 * time.Minute
	fmt.Printf("Duration(30)=%s；30*time.Minute=%s\n", wrongTTL, correctTTL)
	seconds, milliseconds := deadline.Unix(), deadline.UnixMilli()
	wrongFromMillis := time.Unix(milliseconds, 0).UTC()
	wrongFromSeconds := time.UnixMilli(seconds).UTC()
	correctFromMillis, err := contract.ParseUnixMillis(milliseconds)
	if err != nil {
		return err
	}
	_, unitErr := contract.ParseUnixMillis(seconds)
	fmt.Printf("Unix秒=%d Unix毫秒=%d；毫秒塞入Unix的year=%d expired=%t\n", seconds, milliseconds, wrongFromMillis.Year(), contract.Expired(now, wrongFromMillis))
	fmt.Printf("秒塞入UnixMilli=%s expired=%t；正确UnixMilli=%s equal=%t；范围校验err=%v\n", wrongFromSeconds.Format(time.RFC3339), contract.Expired(now, wrongFromSeconds), correctFromMillis.Format(time.RFC3339), correctFromMillis.Equal(deadline.Time), unitErr)

	fmt.Println("\n[E03-D] 进程内单调钟、JSON 墙钟与 Equal")
	start := time.Now()                // 同时携带墙钟与本进程单调钟，单调钟值不跨进程传输。
	end := start.Add(30 * time.Minute) // Add 保留两种读数；不真的等待30分钟。
	encoded, err := json.Marshal(start)
	if err != nil {
		return err
	}
	var restored time.Time
	if err := json.Unmarshal(encoded, &restored); err != nil {
		return err
	}
	fmt.Printf("Now含单调钟标记=%t Add后含标记=%t JSON恢复含标记=%t\n", strings.Contains(start.String(), "m="), strings.Contains(end.String(), "m="), strings.Contains(restored.String(), "m="))
	fmt.Printf("end.Sub(start)=%s；Equal(恢复)=%t；==(恢复)=%t；Round(0)Equal=%t\n", end.Sub(start), start.Equal(restored), start == restored, start.Round(0).Equal(restored))
	fmt.Println("m= 的数值与单调钟测量属于本进程；到期字段落盘后只保留墙钟时刻，不能承诺抗系统改钟。")
	return nil
}
