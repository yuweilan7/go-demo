channel是什么
顾名思义，channel就是一个通信管道，被设计用于实现goroutine之间的通信

Go语言尊崇的设计思想是：以通信的方式来共享内存，而不是通过共享内存来实现通信，channel就是这一思想的体现

下面就让我们走进channel的世界吧
channel的数据结构
channel的功能比较复杂，所以就不会是几个字节就能实现的，所以需要一个复杂的struct来承接channel的作用，也就是下文的hchan结构体

然后要注意channel是直接分配到堆上的，因为channel从设计理念上看，就是用于goroutine之间的通信，作用域和生命周期不会被限制在一个函数中

runtime.hchan的类型定义在源码 src/runtime/chan.go中：
type hchan struct {
    qcount   uint           // channel 环形数组中元素的数量
    dataqsiz uint           // channel 环形数组的容量
    buf      unsafe.Pointer // 指向channel 环形数组的一个指针
    elemsize uint16         //  元素所占的字节数
    closed   uint32         // 是否关闭
    elemtype *_type // 元素类型
    sendx    uint   // send index 下一次写的位置
    recvx    uint   // receive index 下一次读的位置
    recvq    waitq  // list of recv waiters 读等待队列
    sendq    waitq  // list of send waiters 写等待队列
    lock mutex // runtime.mutex，保证channel并发安全
        
}
PS：上面图画错了，recvx表示下一次读，sendx表示下一次写

下面我们来理解一下hchan中的字段

对于channel，我们可以将数据缓存到其中，所以有一个buf数组，用来缓冲数据，又因为channel可以同时提供读写功能，所以我们有sendx 和 recvx分别指向下一次写和下一次读的位置，buf、sendx，recvx 就构成了一个环形数组，每次读或写超过最后一个下标，就会回到下标0处。

然后用qcount表示buf中元素数量，用dataqsiz表示buf的容量。

因为channel设计是用在多个goroutine之间的通信上的，所以需要一把mutex来保护读、写、关闭操作的并发安全

然后因为channel提供一个读和写等待队列（recvq和sendq）来帮助goroutine在未完成读写操作后，可以被阻塞挂起，然后等待channel通信来临时，再被唤醒调度

下面我们看一下recvq 和 sendq的结构，也就是waitq结构体，可以把waitq看作是一个链表构成的队列
type waitq struct {
    first *sudog              // sudog队列的队头指针
    last  *sudog              // sudog队列的队尾指针
}

然后介绍一下sudog这个结构体，sudog可以看作是对阻塞挂起的g的一个封装，然后用多个sudog来构成等待队列


下面看一下sudog结构（只留下主要字段）：
type sudog struct {
    g *g // 绑定的goroutine

    next *sudog // 前后指针
    prev *sudog
    elem unsafe.Pointer // 存储元素的容器

    isSelect bool // 标识是不是因为select操作封装的sudog

    // 为true，表示这个sudog是因为channel通信唤醒的
    // 否则为false，表示这个sudog是因为channel close唤醒的
    success bool
    c        *hchan // 绑定的channel
}

这里关注下elem字段，elem作为收发数据的容器
当向channel发送数据时，elem代表将要写进channel的元素地址
当从channel读取数据时，elem代表要从channel中读取的元素地址
channel操作
channel初始化
Go语言中，我们只能通过make函数来初始化一个channel，runtime会调用runtime.makechan函数来完成channel的初始化工作
源码位于src/runtime/chan.go中：
func makechan(t *chantype, size int) *hchan {
    // channel 元素类型
    elem := t.elem
    
    mem, overflow := math.MulUintptr(elem.size, uintptr(size))

    var c *hchan
    switch {
    case mem == 0:
       // channel无缓冲  or 元素大小为0，只需要分配一个hchan
       c = (*hchan)(mallocgc(hchanSize, nil, true))
       c.buf = c.raceaddr()
    case elem.ptrdata == 0:
       // channel 元素不包含指针，hchan和buf 一起分配
       c = (*hchan)(mallocgc(hchanSize+mem, nil, true))
       c.buf = add(unsafe.Pointer(c), hchanSize)
    default:
       // channel 元素包含指针，hchan和buf 分开分配
       // 因为 申请的span 分为scan 和 noscan，无法一起分配
       c = new(hchan)
       c.buf = mallocgc(mem, elem, true)
    }

    // channel 的一些初始化
    c.elemsize = uint16(elem.size)
    c.elemtype = elem
    c.dataqsiz = uint(size)
    lockInit(&c.lock, lockRankHchan)

    return c
}
makechan函数有两个参数t *chantype, size int，第一个参数代表要创建的channel的元素类型，而第二个参数代表通道环形缓冲的容量大小

可以看出为channel开辟内存分为三种情况：
1.  channel无缓冲 or 元素大小为0：只需要分配hchan本身结构体大小的内存
2. 有缓冲区buf， 但元素不包含指针：hchan和buf 一起分配
3. 有缓冲区buf，且元素包含指针类型：hchan和buf 分开分配
针对channel的不同状态，向channel写入结果如下：
channel写入
下面是往channel中写入一个数据的例子
ch := make(chan int)
ch <- 1    // 往管道里写入1
底层其实就是调用了runtime.chansend函数，源码如下：
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr) bool {
    // c：对应的hchan指针
    // ep：被发送到channel的变量的地址
    // block：发送操作是否阻塞（代表如果send不能立即完成的话，是否阻塞）

    // 判断channel是否为nil
    if c == nil {
       // 如果是非阻塞类型，直接返回falase
       if !block {
          return false
       }

       // 是阻塞类型（永久性挂起）
       gopark(nil, nil, waitReasonChanSendNilChan, traceEvGoStop, 2)
       throw("unreachable")
    }

   // 非阻塞类型channel and channel没关闭 and channel满了
    if !block && c.closed == 0 && full(c) {
       return false
    }

    // 加锁
    lock(&c.lock)

    // channel关闭了，执行写操作，触发panic
    if c.closed != 0 {
       unlock(&c.lock)
       panic(plainError("send on closed channel"))
    }

    // 尝试从读等待队列中取出一个goroutine
    if sg := c.recvq.dequeue(); sg != nil {
       // 读等待队列有goroutine，将写入数据直接交给对应的goroutine
       send(c, sg, ep, func() { unlock(&c.lock) }, 3)
       return true
    }

    // 如果环形数组还有容量可以写入
    if c.qcount < c.dataqsiz {
       // 通过sendx 找到写入位置的地址
       qp := chanbuf(c, c.sendx)
       if raceenabled {
          racenotify(c, c.sendx, nil)
       }
       // 将ep中的数据写入到qp中
       typedmemmove(c.elemtype, qp, ep)
       c.sendx++
       // 如果sendx == dataqsiz，因为是缓存数组，如果将snedx置为0
       if c.sendx == c.dataqsiz {
          c.sendx = 0
       }
       // channel 中的元素数量+1
       c.qcount++
       unlock(&c.lock)
       return true
    }

    // 非阻塞类型的写操作走到这一步，不管有没有写入到channel中，都不需要阻塞，直接return
    if !block {
       unlock(&c.lock)
       return false
    }

    // Block on the channel. Some receiver will complete our operation for us.
    gp := getg()

    // 取出一个sudog结构
    mysg := acquireSudog()
    mysg.releasetime = 0
    if t0 != 0 {
       mysg.releasetime = -1
    }

    // 设置对应状态
    mysg.elem = ep
    mysg.waitlink = nil
    // 绑定goroutine
    mysg.g = gp
    mysg.isSelect = false
    // 绑定channel
    mysg.c = c
    gp.waiting = mysg
    gp.param = nil
    // 进入写等待队列
    c.sendq.enqueue(mysg)
    gp.parkingOnChan.Store(true)

    // gopark操作
    gopark(chanparkcommit, unsafe.Pointer(&c.lock), waitReasonChanSend, traceEvGoBlockSend, 2)
   
    gp.waiting = nil
    gp.activeStackChans = false
    closed := !mysg.success
    gp.param = nil
    if mysg.releasetime > 0 {
       blockevent(mysg.releasetime-t0, 2)
    }
    mysg.c = nil
    releaseSudog(mysg)
    if closed {
       if c.closed == 0 {
          throw("chansend: spurious wakeup")
       }
       panic(plainError("send on closed channel"))
    }
    return true
}

往channel发送数据会出现三种情况
case1：channel中有读等待goroutine
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr) bool {
    // ........
    // 加锁
    lock(&c.lock)
    
    // 尝试从读等待队列中取出一个goroutine
    if sg := c.recvq.dequeue(); sg != nil {
       // 读等待队列有goroutine，将写入数据直接交给对应的goroutine
       send(c, sg, ep, func() { unlock(&c.lock) }, 3)
       return true
    }
    
    // ........
}

func send(c *hchan, sg *sudog, ep unsafe.Pointer, unlockf func(), skip int) {
    // 将ep 复制到 sg对应的elem上
    if sg.elem != nil {
       sendDirect(c.elemtype, sg, ep)
       sg.elem = nil
    }
    gp := sg.g
    // 释放锁（因为写操作已经完成了）
    unlockf()
    gp.param = unsafe.Pointer(sg)

    sg.success = true
    if sg.releasetime != 0 {
       sg.releasetime = cputicks()
    }

    // 唤醒goroutine
    goready(gp, skip+1)
    
}
- 先拿锁
- 从recvq（读等待队列）里面弹出队列头部的sudog，进入send流程
- 将要写入的数据拷贝得到这个sudog对应的elem数据容器上
- 释放锁
- 唤醒sudog绑定的goroutine（也就是将这个goroutine重新放入到gmp模型中，等待调度）

case2：channel中没有读等待goroutine，并且环形缓冲数组里面有剩余空间
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr) bool {
    // ........
    // 加锁
    lock(&c.lock)
    // 如果环形数组还有容量可以写入
    if c.qcount < c.dataqsiz {
        // 通过sendx 找到写入位置的地址
        qp := chanbuf(c, c.sendx)
        if raceenabled {
           racenotify(c, c.sendx, nil)
        }
        // 将ep中的数据写入到qp中
        typedmemmove(c.elemtype, qp, ep)
        c.sendx++
        // 如果sendx == dataqsiz，因为是缓存数组，如果将snedx置为0
        if c.sendx == c.dataqsiz {
           c.sendx = 0
        }
        // channel 中的元素数量+1
        c.qcount++
        unlock(&c.lock)
        return true
     }
     // ..............
}
- 先拿锁
- 将数据写入到 sendx指向的位置中
- sendx++，qcount++
- 释放锁

case3：channel中没有读等待goroutine，并且无剩余空间存放数据
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr) bool {
    // .......
    gp := getg()
    // 取出一个sudog结构
    mysg := acquireSudog()
    // 将ep存入到elem中
    mysg.elem = ep
    // 绑定goroutine
    mysg.g = gp
    // 绑定channel
    mysg.c = c
    // 进入写等待队列
    c.sendq.enqueue(mysg)
    
    // gopark操作
    gopark(chanparkcommit, unsafe.Pointer(&c.lock), waitReasonChanSend, traceEvGoBlockSend, 2)
    
    // 处理状态
    gp.waiting = nil
    gp.activeStackChans = false
    closed := !mysg.success
    gp.param = nil
    mysg.c = nil
    
    // 回收sudog
    releaseSudog(mysg)
    return true

}
- 锁保护步骤同样有的
- 获取一个sudog结构，绑定对应的channel，goroutine，还有ep指针
- 将sudog放入channel的写等待队列（sendq）
- runtime.gopark（挂起当前goroutine，可以看作是解绑当前g和m，然后开启下一轮调度）

 特殊case
第一种特殊情况：写入的channel为nil
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr) bool {
    // c：对应的hchan指针
    // ep：被发送到channel的变量的地址
    // block：发送操作是否阻塞（代表如果send不能立即完成的话，是否阻塞）
    
    // 判断channel是否为nil
    if c == nil {
       // 如果是非阻塞类型，直接返回falase
       if !block {
          return false
       }
        
       // 是阻塞类型（永久性挂起）
       gopark(nil, nil, waitReasonChanSendNilChan, traceEvGoStop, 2)
       throw("unreachable")
    }
    
    // ............    
}
- 当channel为nil的时候，对channel进行写操作，会导致当前goroutine永久性挂起，如果当前goroutine是main goroutine的话，还会导致整个程序退出

第二种特殊情况：channel已经关闭，还想进行写操作
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr) bool {

    // 加锁
    lock(&c.lock)
   // channel关闭了，执行写操作，触发panic
   if c.closed != 0 {
        unlock(&c.lock)
        panic(plainError("send on closed channel"))
    }
    // ............    
}
- 当channel已经被关闭，再向channel写数据，会出现panic
channel读取
从channel读取数据的编码形式如下
ch := make(chan, int)
v := <- ch       //  直接读取
v, ok <- ch      //  ok判断读取的v是否有效
底层其实就是调用了runtime.chanrecv函数，源码如下：
func chanrecv(c *hchan, ep unsafe.Pointer, block bool) (selected, received bool) {
    // c：对应的hchan指针
    // ep：被发送到channel的变量的地址
    // block：发送操作是否阻塞（代表如果send不能立即完成的话，是否阻塞）

    // selected和received
    // 如果received为true，则说明数据是从channel接收到的
    // 如果received为false，selected为true，说明channel是通道关闭，并且得到零值
    // 如果received为false，selected为false，则是因为非阻塞操作返回

    // 判断channel是否为nil
    if c == nil {
       // 如果是非阻塞类型，直接返回
       if !block {
          // 两个false，通道不想阻塞而返回
          return false, false
       }
       // 是阻塞类型（永久性挂起）
       gopark(nil, nil, waitReasonChanReceiveNilChan, traceEvGoStop, 2)
       throw("unreachable")
    }

    // 非阻塞类型，并且channel是空的（无缓冲，且sendq为空）
    if !block && empty(c) {
       if atomic.Load(&c.closed) == 0 {
          // 通道没有关闭，返回两个false，通道不想阻塞而返回
          return
       }
       if empty(c) {
          // 将ep清空
          if ep != nil {
             typedmemclr(c.elemtype, ep)
          }
          // 通道关闭了，但是channel是空的（无缓冲，且sendq为空），返回true，false，表示channel因为通道为空，收到零值
          return true, false
       }
    }

    // 加锁
    lock(&c.lock)

    // 如果channel已经关闭
    if c.closed != 0 {
       // 通道已经关闭，返回两个false，通道不想阻塞而返回
       if c.qcount == 0 {
          if raceenabled {
             raceacquire(c.raceaddr())
          }
          unlock(&c.lock)
          // 清空ep
          if ep != nil {
             typedmemclr(c.elemtype, ep)
          }
          // 返回false，false，说明channel关闭，并且得到零值
          return true, false
       }
    } else {
       // 如果通道没有关闭，并且写队列里面有 等待的goroutine
       if sg := c.sendq.dequeue(); sg != nil {
          recv(c, sg, ep, func() { unlock(&c.lock) }, 3)
          return true, true
       }
    }

    // channel中有元素
    if c.qcount > 0 {
       // Receive directly from queue
       qp := chanbuf(c, c.recvx)
       // 将recvx上的 数据 写入到ep中
       if ep != nil {
          typedmemmove(c.elemtype, ep, qp)
       }
       // 清空recvx上的数据
       typedmemclr(c.elemtype, qp)
       // index++
       c.recvx++
       // 环形数组操作
       if c.recvx == c.dataqsiz {
          c.recvx = 0
       }
       // 元素数量--
       c.qcount--
       unlock(&c.lock)
       // 返回true，true，说明channel中有元素，并且channel没有关闭
       return true, true
    }

    if !block {
       unlock(&c.lock)
       // 通道不想阻塞而返回
       return false, false
    }

    gp := getg()
    mysg := acquireSudog()
    mysg.elem = ep
    mysg.waitlink = nil
    gp.waiting = mysg
    mysg.g = gp
    mysg.isSelect = false
    mysg.c = c
    gp.param = nil
    // 包装sudog进入读队列（recvq）
    c.recvq.enqueue(mysg)

    gp.parkingOnChan.Store(true)
    // 挂起当前gorotine，等待goready唤醒
    gopark(chanparkcommit, unsafe.Pointer(&c.lock), waitReasonChanReceive, traceEvGoBlockRecv, 2)
    
    // 清空一些状态
    gp.waiting = nil
    gp.activeStackChans = false
    success := mysg.success
    gp.param = nil
    mysg.c = nil
    // 回收sudog
    releaseSudog(mysg)
    return true, success
}
往channel读取数据会出现三种情况

case1：channel中有写等待goroutine
func chanrecv(c *hchan, ep unsafe.Pointer, block bool) (selected, received bool) {
    // c：对应的hchan指针
    // ep：被发送到channel的变量的地址
    // ....
   
    // 加锁
    lock(&c.lock)
    
    // 如果通道没有关闭，并且写队列里面有 等待的goroutine
    if sg := c.sendq.dequeue(); sg != nil {
        // Found a waiting sender. If buffer is size 0, receive value
        // directly from sender. Otherwise, receive from head of queue
        // and add sender's value to the tail of the queue (both map to
        // the same buffer slot because the queue is full).
        recv(c, sg, ep, func() { unlock(&c.lock) }, 3)
        return true, true
    }
    
    // ........
 
}


func recv(c *hchan, sg *sudog, ep unsafe.Pointer, unlockf func(), skip int) {
    if c.dataqsiz == 0 {
       if ep != nil {
          // channel无容量，将sudog 对应的数据写给ep
          recvDirect(c.elemtype, sg, ep)
       }
    } else {
       // 到这一步，channel 环形数组一定是满的（因为sendq里面有等待者）
       // recvx对应的位置的地址
       qp := chanbuf(c, c.recvx)
       // 将qp上的数据(环形缓冲recvx 指向的数据) 写入ep
       if ep != nil {
          typedmemmove(c.elemtype, ep, qp)
       }
       // 并且sudug上的数据写入 qp
       typedmemmove(c.elemtype, qp, sg.elem)
       // 读index++
       c.recvx++

       // 环形数组处理
       if c.recvx == c.dataqsiz {
          c.recvx = 0
       }
       // 同步sendx 和 recvx
       c.sendx = c.recvx // c.sendx = (c.sendx+1) % c.dataqsiz
    }
    // 重置这个sudog状态
    sg.elem = nil
    gp := sg.g
    
    // 释放锁
    unlockf()
    gp.param = unsafe.Pointer(sg)
    sg.success = true
    if sg.releasetime != 0 {
       sg.releasetime = cputicks()
    }
    // 唤醒对应的写等待gorotuine
    goready(gp, skip+1)
}
- 先拿锁
- 从sendq（写等待队列）里面弹出队列头部的sudog，进入recv流程
- 如果channel无缓冲区，直接读取sudog里面的数据，并唤醒sudog对应goroutine
- 如果channel有缓冲区，读取环形缓冲区头部元素，并将sudog中的元素写入到缓冲区，唤醒sudog对应goroutine
- 释放锁
case2：channel中没有写等待goroutine，并且环形缓冲数组里面有剩余元素
func chanrecv(c *hchan, ep unsafe.Pointer, block bool) (selected, received bool) {
    // c：对应的hchan指针
    // ep：被发送到channel的变量的地址
    // .....
    
    // 加锁
    lock(&c.lock)
    
    // channel中有元素
    if c.qcount > 0 {
        // 取recvx对应地址上的元素
        qp := chanbuf(c, c.recvx)
        // 将这个元素 写入到ep中
        if ep != nil {
           typedmemmove(c.elemtype, ep, qp)
        }
        // 清空recvx上的数据
        typedmemclr(c.elemtype, qp)
        // index++
        c.recvx++
        // 环形数组操作
        if c.recvx == c.dataqsiz {
           c.recvx = 0
        }
        // 元素数量--
        c.qcount--
        unlock(&c.lock)
        // 返回true，true，说明channel中有元素，并且channel没有关闭
        return true, true
    }
    
    // .....
 }
- 先拿锁
- 读取recvx指向的数据
- recvx++，qcount--
- 释放锁
case3：channel中没有写等待goroutine，并且环形缓冲数组里面无剩余元素
func chanrecv(c *hchan, ep unsafe.Pointer, block bool) (selected, received bool) {
    // c：对应的hchan指针
    // ep：被发送到channel的变量的地址
    // .....
    
    // 加锁
    lock(&c.lock)  
    // 获取当前goroutine
    gp := getg()
    // 获取一个sudog
    mysg := acquireSudog()
    // 绑定接收指针
    mysg.elem = ep
    gp.waiting = mysg
    // 绑定goroutine
    mysg.g = gp
    // 绑定channel
    mysg.c = c
    // 包装sudog进入读队列（recvq）
    c.recvq.enqueue(mysg)
    // 挂起当前gorotine，等待goready唤醒
    gopark(chanparkcommit, unsafe.Pointer(&c.lock), waitReasonChanReceive, traceEvGoBlockRecv, 2)

    // 清空channel状态
    gp.waiting = nil
    gp.activeStackChans = false
    success := mysg.success
    gp.param = nil
    mysg.c = nil
    // 回收sudog
    releaseSudog(mysg)
    return true, success
}
- 锁保护步骤同样有的
- 获取一个sudog结构，绑定对应的channel，goroutine，还有ep指针
- 将sudog放入channel的读等待队列（recvq）
- runtime.gopark（挂起当前goroutine，可以看作是解绑当前g和m，然后开启下一轮调度）

特殊case
第一种特殊情况：读取的channel为nil
func chanrecv(c *hchan, ep unsafe.Pointer, block bool) (selected, received bool) {
    
    // 判断channel是否为nil
    if c == nil {
       // 如果是非阻塞类型，直接返回
       if !block {
          // 两个false，通道不想阻塞而返回
          return false, false
       }
       // 是阻塞类型（永久性挂起）
       gopark(nil, nil, waitReasonChanReceiveNilChan, traceEvGoStop, 2)
       throw("unreachable")
    }
    
     // .....   
 }
- 当channel为nil的时候，对channel进行读操作，会导致当前goroutine永久性挂起，如果当前goroutine是main goroutine的话，还会导致整个程序退出
第二种特殊情况：channel已经关闭，并且没有buf里面没有元素
func chanrecv(c *hchan, ep unsafe.Pointer, block bool) (selected, received bool) {
    // ......
    
    // 如果channel已经关闭
    if c.closed != 0 {
        // 通道已经关闭，返回两个false，通道不想阻塞而返回
        if c.qcount == 0 {
           unlock(&c.lock)
           // 清空ep
           if ep != nil {
              typedmemclr(c.elemtype, ep)
           }
           // 返回false，false，说明channel关闭，并且得到零值
           return true, false
        }
        // ......
     }

}
- channel已经关闭，并且没有剩余元素，还想读取channel会得到对应类型的零值
channel关闭
管道的关闭很简单，操作如下
ch := make(chan int)
close(ch)
底层其实就是调用了runtime.closechan函数，源码如下：
func closechan(c *hchan) {
    // chan为nil，想要执行关闭操作，直接panic
    if c == nil {
       panic(plainError("close of nil channel"))
    }

    lock(&c.lock)
    if c.closed != 0 {
       unlock(&c.lock)
       // 通道已经关闭，再次执行关闭的话，直接panic
       panic(plainError("close of closed channel"))
    }
    // 通道置为1，表示关闭
    c.closed = 1

    // 通过一个gList来记录channel中所有goroutine等待者
    var glist gList

    // 将所有recvq的等待者加入到glist中
    for {
       sg := c.recvq.dequeue()
       if sg == nil {
          break
       }
       if sg.elem != nil {
          typedmemclr(c.elemtype, sg.elem)
          sg.elem = nil
       }
       gp := sg.g
       gp.param = unsafe.Pointer(sg)
       sg.success = false
       glist.push(gp)
    }

    // 将所有sendq的等待者加入到glist中
    for {
       sg := c.sendq.dequeue()
       if sg == nil {
          break
       }
       sg.elem = nil
       gp := sg.g
       gp.param = unsafe.Pointer(sg)
       sg.success = false
       glist.push(gp)
    }
    unlock(&c.lock)

    // 依次唤醒glist中所有等待者
    for !glist.empty() {
       gp := glist.pop()
       gp.schedlink = 0
       goready(gp, 3)
    }
}
这里我们直接按照源码来分析流程
- 如果对一个nil的channel执行close操作，会发生panic
- 加锁
- 如果重复关闭channel，也会panic
- 关闭channel（c.closed置为1）
- 将sendq和recvq里面所有等待者加入到glist中
- 唤醒glist中所有等待者（唤醒sudog对应的goroutine）
Select
select也被称为多路select，指的是一个goroutine 可以服务多个 channel的读或写操作

源码较长，这里只总结下select的原理：

select分为两种，包含非阻塞型select(包含default分支的） 和 阻塞型select（不包含default分支的）
阻塞型例子：
package main

func main() {
    ch := make(chan int)

    select {
    case <-ch:

    case ch <- 1:
    }

}

非阻塞型例子：
package main

func main() {
    ch := make(chan int)

    select {
    case <-ch1:

    case ch2 <- 1:
    

    default:
       
    }

}
select的核心原理是，按照随机的顺序执行case，直到某个case完成操作，如果所有case的都没有完成操作，则看有没有default分支，如果有default分支，则直接走default，防止阻塞

如果没有的话，需要将当前goroutine 加入到所有case对应channel的等待队列中，并挂起当前goroutine，等待唤醒。

如果当前goroutine被某一个case 上的channel操作唤醒后，还需要将当前goroutine从所有case对应channel的等待队列中剔除

检验
channel 是线程安全的吗？
一般来说，我们对channel就只有读，写，关闭三种操作，这三种操作，channel底层数据结构都用同一把runtime.Mutex来进行保护
channel 的底层实现原理 
channel的底层结构体叫做runtime.hchan
- 拥有一把runtime.mutex来保证channel进行读，写，关闭操作逻辑的并发安全
- 通过读写指针（sendx，recvx）和一个buf数组 实现一个环形缓冲队列，可以让channel拥有存储数据的能力
- 拥有读写等待队列，当一个goroutine对channel进行读或写操作，操作无法及时完成的时候，可以进入到等待队列 等待，当前goroutine也被runtime.gopark进行挂起
- 而读写操作也能取出 等待队列里面的goroutine，通过runtime.goready将等待中goroutine唤醒（放入gmp模型中），等待GMP的调度
对channel 进行读、写、关闭操作会怎么样？

1. 对nil的channel进行读和写 都会造成当前goroutine永久阻塞（如果当前goroutine是main goroutine，则会让整个程序直接报fatal error 退出），关闭则会发生panic（但这里我们不要忘记，还有一种叫做非阻塞的方式操作channel，这种模式下，就算对为nil的channel读写，也不会阻塞的）
package main

import (
    "time"
)

// 为nil的channel
var ch chan int

func main() {
    // 对nil channel进行读操作
    // receiveExample1()
    // receiveExample2()

    // 对nil channel进行写操作
    // sendExample1()
    // sendExample2()

    // 对nil channel进行close操作
    // close(ch)

    // 非阻塞模式
    // select {
    // case <-ch:
    //     fmt.Println("1")
    // default:
    //     fmt.Println("default")
    // }

    time.Sleep(1 * time.Second)

}

// 在主goroutine对nil channel进行读
func receiveExample1() {
    <-ch
}

// 在普通goroutine对nil channel进行读
func receiveExample2() {
    go func() {
       <-ch
    }()
}

// 在主goroutine对nil channel进行写
func sendExample1() {
    ch <- 1
}

// 在普通goroutine对nil channel进行写
func sendExample2() {
    go func() {
       ch <- 1
    }()
}
2. 对不为nil，并且未关闭的channel操作，读和写都有两种情况
  1. 读操作：
    1. 成功读取：如果channel中有数据，直接从channel里面读取，如果此时写等待队列里面有goroutine，还需要将队列头部goroutine数据写入到channel中，并唤醒这个goroutine； 如果channel没有数据，就尝试从写等待队列中读取数据，并做对应的唤醒操作
    2. 阻塞挂起(读操作无法及时完成)：channel里面没有数据 并且 写等待队列为空，则当前goroutine 加入 读等待队列中，并挂起，等待唤醒
  2. 写操作
    1. 成功写入：如果channel 读等待队列不为空，则取 头部goroutine，将数据直接复制给这个头部goroutine，并将其唤醒，流程结束； 否则就尝试将数据写入到channel 环形缓冲中
    2. 阻塞挂起(写操作无法及时完成)：通道里面buf满了 并且 读等待队列为空，则当前gorotine 加入写等待队列中，并挂起，等待唤醒
3. 对已经关闭的channel进行写和关闭 都会导致panic，而读取是直到读完channel中剩余数据，还想读的话，就会获得零值

运行下面代码，会得到什么结果？
package main

import "fmt"

func main() {
    case1 := make(chan int)
    case2 := make(chan int)
    close(case1)
    close(case2)

    select {
    case <-case1:
       fmt.Print("case1")
    case case2 <- 1:
       fmt.Print("case2")
    default:
       fmt.Print("default")
    }
}
A: 进入default分支，打印"default"
B: 进入case1分支，打印"case1"
C: 进入case2分支，打印"case2"
D: 程序panic
E: 程序可能panic，也可能打印"case1"

答案：E
运行下面代码，会得到什么结果？
package main

func main() {
    c := make(chan int, 1)

    done := false
    for !done {
       select {
       case <-c:
          print(1)
          c = nil
       case c <- 1:
          print(2)
       default:
          print(3)
          done = true
       }
    }
}

答案：2 1 3
结语
本文，我们对channel读、写、关闭做了源码分析，以及简单介绍了select原理
 至此，channel的源码剖析完结，有什么疑问，记得在评论划线提出哦