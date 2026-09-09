// defer 示例集中演示 Go 中延迟调用的核心语义：
//  1. 执行 defer 语句时，立即计算并保存函数值和调用实参；
//  2. 包围它的函数退出时，按后注册、先执行（LIFO）的顺序真正调用；
//  3. return 会先确定结果值，再执行 defer，最后把结果交给调用者；
//  4. panic 展开当前 goroutine 的调用栈时，也会执行沿途已经注册的 defer。
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	fmt.Println("=== 1. defer 在函数退出前执行，并且顺序是 LIFO ===")
	demoOrder()

	fmt.Println("\n=== 2. 普通实参立即求值；闭包到执行时才读取变量 ===")
	demoArgumentEvaluation()
	demoPointerArgument()

	fmt.Println("\n=== 3. 用 defer 释放已经成功取得的资源 ===")
	demoFileCopy()

	fmt.Println("\n=== 4. return、具名结果参数与 defer ===")
	fmt.Printf("具名结果被 defer 修改：%d\n", namedResult())
	fmt.Printf("匿名结果不受局部变量后续修改影响：%d\n", unnamedResult())
	fmt.Printf("具名结果存在，但 defer 只改局部变量：%d\n", namedResultButChangeLocal())

	fmt.Println("\n=== 5. panic 时执行 defer，并在当前 goroutine 中 recover ===")
	demoRecover()
	fmt.Println("recover 后，调用者可以继续执行")
}

func demoOrder() {
	// defer 后面必须是函数或方法调用，而不是一个函数值：
	//
	//     defer first       // 编译错误：expression in defer must be function call
	//     defer first()     // 正确
	//     defer (first())   // 编译错误：defer 的调用表达式不能整体加括号
	//
	// 注册发生在程序执行到 defer 语句时；没有执行到的分支不会注册 defer。
	defer fmt.Println("defer 1：最先注册，最后执行")
	defer fmt.Println("defer 2：第二个注册，第二个执行")
	defer fmt.Println("defer 3：最后注册，最先执行")

	fmt.Println("函数体：三个 defer 已注册，函数即将结束")
	// 这个函数没有显式写 return。执行到函数体末尾也属于函数返回，仍会执行 defer。
}

func demoArgumentEvaluation() {
	number := 1

	// 执行这条 defer 语句时，fmt.Printf 函数值、格式字符串和 number 的当前值 1
	// 都已经保存。真正调用 Printf 虽然发生在函数退出时，但它收到的仍然是 1。
	defer fmt.Printf("普通参数保存的是注册时的值：%d\n", number)

	// 这个 defer 的调用没有传入 number。匿名函数形成闭包，并在稍后执行函数体时
	// 读取变量 number，所以它会看到赋值后的 2。
	defer func() {
		fmt.Printf("闭包执行时读取变量：%d\n", number)
	}()

	number = 2
	fmt.Println("函数体把 number 改为 2")
}

func demoPointerArgument() {
	array := [4]int{1, 2, 3, 4}

	// &array 这个指针值在注册 defer 时就确定了。稍后 array[0] 被修改，但指针仍指向
	// 同一个数组，因此延迟函数会通过这个指针看到修改后的元素。
	defer printArray(&array)

	array[0] = 100
	fmt.Println("函数体把 array[0] 改为 100")
}

func printArray(array *[4]int) {
	fmt.Printf("通过已保存的指针读取数组：%v\n", *array)
}

// copyFile 在每个资源成功打开后立刻安排 Close。这样无论后面正常返回，还是中途
// 因错误返回，相应资源都会关闭。dst 后打开，所以它会先关闭，然后才关闭 src。
//
// 返回值使用名字，是为了让延迟函数在 Close 失败、且前面没有错误时，把关闭错误
// 传给调用者。简单示例经常直接写 defer file.Close()；那样会忽略 Close 的错误。
func copyFile(dstFile, srcFile string) (written int64, err error) {
	src, err := os.Open(srcFile)
	if err != nil {
		return 0, fmt.Errorf("打开源文件: %w", err)
	}
	defer func() {
		if closeErr := src.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("关闭源文件: %w", closeErr)
		}
	}()

	dst, err := os.Create(dstFile)
	if err != nil {
		// dst 创建失败也会返回；前面已经注册的 src.Close 仍然会执行。
		return 0, fmt.Errorf("创建目标文件: %w", err)
	}
	defer func() {
		if closeErr := dst.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("关闭目标文件: %w", closeErr)
		}
	}()

	written, err = io.Copy(dst, src)
	if err != nil {
		return written, fmt.Errorf("复制文件内容: %w", err)
	}
	return written, nil
}

func demoFileCopy() {
	tempDir, err := os.MkdirTemp("", "go-defer-demo-")
	if err != nil {
		fmt.Printf("创建临时目录失败：%v\n", err)
		return
	}
	defer os.RemoveAll(tempDir)

	srcPath := filepath.Join(tempDir, "source.txt")
	dstPath := filepath.Join(tempDir, "destination.txt")
	const content = "defer makes cleanup follow the resource"

	if err := os.WriteFile(srcPath, []byte(content), 0o600); err != nil {
		fmt.Printf("写入示例源文件失败：%v\n", err)
		return
	}

	written, err := copyFile(dstPath, srcPath)
	if err != nil {
		fmt.Printf("复制失败：%v\n", err)
		return
	}

	copied, err := os.ReadFile(dstPath)
	if err != nil {
		fmt.Printf("读取复制结果失败：%v\n", err)
		return
	}
	fmt.Printf("复制 %d 字节，目标内容：%q\n", written, copied)

	// 不要在一个很长的循环里不断 defer 关闭资源：那些调用要等包围整个循环的函数
	// 返回才执行，资源可能积压。把单次迭代提取成辅助函数，让每次迭代及时返回即可。
}

func namedResult() (result int) {
	number := 1

	// result 是具名结果参数，也是函数内的局部变量。return number 会先把 1 赋给
	// result，然后才执行这个 defer，因此这里能把最终结果改成 2。
	defer func() {
		result++
	}()

	return number
}

func unnamedResult() int {
	number := 1

	// 匿名返回值可以用一个调用者不可见的临时结果来理解：return number 先把 1
	// 复制给临时结果；defer 随后把局部变量 number 改成 2，却不会改到临时结果。
	defer func() {
		number++
	}()

	return number
}

func namedResultButChangeLocal() (result int) {
	number := 1

	// 函数虽然声明了具名结果 result，但这个 defer 修改的是另一个变量 number。
	// return number 已经把 1 赋给 result，所以最终返回值仍然是 1。
	defer func() {
		number++
	}()

	return number
}

func demoRecover() {
	// 这个 defer 先注册，所以在 LIFO 顺序中最后执行。即使发生 panic，它也会运行。
	defer fmt.Println("较早注册的清理 defer 仍然执行")

	defer func() {
		// recover 必须由延迟函数直接调用，并且只能恢复同一个 goroutine 正在展开的
		// panic。在没有 panic 的普通路径上调用 recover，会得到 nil。
		if value := recover(); value != nil {
			fmt.Printf("recover 捕获：%v\n", value)
		}
	}()

	fmt.Println("即将触发 panic")
	panic("模拟不可继续的故障")
	// panic 之后的普通语句不会执行。recover 结束的是栈展开过程；执行会从
	// demoRecover 的调用者继续，而不会跳回这里的 panic 下一行。
}
