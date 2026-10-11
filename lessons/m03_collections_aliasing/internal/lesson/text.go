package lesson

import (
	"fmt"
	"unicode/utf8"
)

// PrefixRunes 由文本实验和测试调用，按 Unicode 码点数量截取有效 UTF-8 商品名。
// string 本身允许任意字节，本函数的业务契约主动要求有效 UTF-8；不让坏字节静默变成 RuneError。
// 返回值由调用者接收，输入是不可变 string，因此没有外部可变存储需要本函数清理。
func PrefixRunes(text string, count int) (string, error) {
	if count < 0 {
		return "", fmt.Errorf("码点上限不能为负数: %d", count)
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("商品名不是有效 UTF-8")
	}
	// []rune 把 UTF-8 解码为码点值；len(runes) 是码点数量，不是字节数，也不保证等于视觉字符数。
	runes := []rune(text)
	if count >= len(runes) {
		return text, nil // 数量足够，原 immutable string 可安全返回，不必做冗余复制。
	}
	return string(runes[:count]), nil
}

// textExperiment 由 Run 同步调用，演示字节截断错误、码点截断修复和安全转换的可观察隔离。
// 所有变更均在局部数据中；本课不用 unsafe，也不把编译器可优化的分配细节当成语义。
func textExperiment(t *transcript) error {
	t.log("\n[E07] 中文商品名：字节、码点与转换后的修改边界")
	name := "库存A"
	bad := name[:4] // 按字节截取：库存各3字节，这里只留下“存”的第1个字节。
	good, err := PrefixRunes(name, 2)
	if err != nil {
		return err
	}
	t.log("name=%q bytes=%d runes=%d; 错误 [:4]=%q bytes=% x validUTF8=%v; 修复前2码点=%q", name, len(name), utf8.RuneCountInString(name), bad, []byte(bad), utf8.ValidString(bad), good)
	positions := make([]string, 0, 3)
	for byteIndex, codePoint := range name {
		positions = append(positions, fmt.Sprintf("%d:%c", byteIndex, codePoint))
	}
	t.log("range string 的字节起点: %v", positions)
	if err := require(len(name) == 7 && utf8.RuneCountInString(name) == 3 && !utf8.ValidString(bad) && good == "库存", "E07: 中文字节数和 UTF-8 截断边界"); err != nil {
		return err
	}

	bytes := []byte(name)
	// string 转 []byte 后可以修改结果；语言要求结果修改不能改变原 string。
	// 是否真分配内存可能被编译器优化，业务只依赖可观察的隔离，不探测 unsafe 指针。
	bytes[0] = 'X'
	t.log("改 []byte: 原 string=%q，改后 bytes=% x", name, bytes)
	buffer := []byte("库存A")
	immutable := string(buffer)
	buffer[6] = 'B'
	t.log("[]byte 转 string 后再改源: string=%q 源 bytes=%q", immutable, buffer)
	points := []rune(name)
	points[0] = '仓'
	t.log("改 []rune: 原=%q 新=%q", name, string(points))
	if err := require(name == "库存A" && immutable == "库存A" && string(buffer) == "库存B" && string(points) == "仓存A", "E07: 安全转换的结果不应形成可变别名"); err != nil {
		return err
	}

	empty, emptyErr := PrefixRunes("", 2)
	zero, zeroErr := PrefixRunes(name, 0)
	_, invalidErr := PrefixRunes(string([]byte{0xff}), 1)
	_, negativeErr := PrefixRunes(name, -1)
	t.log("边界: 空文本=%q err=%v; 上限0=%q err=%v; 非UTF8 err=%v; 负数 err=%v", empty, emptyErr, zero, zeroErr, invalidErr, negativeErr)
	if err := require(empty == "" && emptyErr == nil && zero == "" && zeroErr == nil && invalidErr != nil && negativeErr != nil, "E07: 空文本/0/非法文本/负数边界"); err != nil {
		return err
	}
	t.log("E07 PASS：rune 不是完整视觉字符；emoji/组合字符的展示截断需另定字素契约")
	return nil
}
