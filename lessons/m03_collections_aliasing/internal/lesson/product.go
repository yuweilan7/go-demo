// Package lesson 用同一批合成库存讲解副本、别名和所有权，不连接真实缓存或库存库。
package lesson

import (
	"maps"
	"slices"
)

// Product 是库存快照中的一项。SKU、Stock 的赋值复制值；Tags 的赋值复制切片描述值；
// Meta 的赋值仍引用同一个 map。因此“Product 是 struct 值”不等于它的可变内容全部独立。
type Product struct {
	SKU   string
	Stock int
	Tags  []string
	Meta  map[string]string
}

// NewInventory 由实验和测试调用，每次创建一套独立的合成库存。
// 每项 Tags/Meta 也在这次调用里创建，避免不同实验因复用全局数据互相污染。
func NewInventory() []Product {
	return []Product{
		{SKU: "A", Stock: 5, Tags: []string{"热卖"}, Meta: map[string]string{"warehouse": "深圳"}},
		{SKU: "B", Stock: 0, Tags: []string{"缺货"}, Meta: map[string]string{"warehouse": "深圳"}},
		{SKU: "C", Stock: 7, Tags: []string{"新品"}, Meta: map[string]string{"warehouse": "广州"}},
	}
}

// CloneProduct 由快照过滤和缓存的边界调用，把这一项的可变内容交给新的 owner。
// p 参数先接收 Product 值副本，下面再分别复制它引用的 Tags 和 Meta。
// 当前元素都是 string，没有更深的可变对象；若 Meta 改成 map[string][]byte，还需继续复制 []byte。
func CloneProduct(p Product) Product {
	p.Tags = slices.Clone(p.Tags) // Clone 保留 nilness；只复制这一层 string 元素。
	p.Meta = maps.Clone(p.Meta)   // map 容器独立；这里 string 值不可变，所以已经够用。
	return p
}

// CloneProducts 为调用方创建可独立修改的库存快照，调用方拥有返回的 slice 和嵌套对象。
// 本课约定 nil 输入仍返回 nil，非 nil 空列表仍返回非 nil 空列表，避免悄悄改变 API 的空值语义。
func CloneProducts(products []Product) []Product {
	if products == nil {
		return nil
	}
	result := make([]Product, len(products))
	for i, product := range products {
		result[i] = CloneProduct(product)
	}
	return result
}

// FilterInPlaceBad 由 core 实验调用，刻意违反“输入被调用方和缓存共同持有”的契约。
// products 参数只是 slice 描述值的副本，products[:0] 的 len=0，但底层数组和 cap 没变。
// 每次 append 的位置都从数组开头开始，因而会覆盖调用方仍在使用的库存。
func FilterInPlaceBad(products []Product) []Product {
	result := products[:0]
	for _, product := range products {
		if product.Stock > 0 {
			result = append(result, product)
		}
	}
	return result
}

// FilterSnapshot 是“调用者保留输入，结果交给另一个 owner”时的修复。
// result 的底层数组是新分配的，每个入选元素再由 CloneProduct 复制可变嵌套内容。
// 返回 []Product 而非 []*Product：指针不会自动解决共享问题，库存快照无需指针身份。
func FilterSnapshot(products []Product) []Product {
	if products == nil {
		return nil
	}
	result := make([]Product, 0, len(products))
	for _, product := range products {
		if product.Stock > 0 {
			result = append(result, CloneProduct(product))
		}
	}
	return result
}

// FilterOwned 是另一种合法修复：调用者明确把整批可变库存的唯一所有权交给本函数。
// 它仍复用数组，要求调用者不能再使用旧描述值或旧元素别名；这条约定靠 API 设计/审查维护。
// 与 FilterSnapshot 的选择取决于业务所有权，并非“所有 in-place 过滤都错误”。
func FilterOwned(products []Product) []Product {
	result := FilterInPlaceBad(products)
	// Product 包含 Tags/Meta 引用。截短 len 不会自动清掉尾部，clear 移除尾部过期引用。
	// clear 同样会被其他数组别名看到，所以只有独占输入时才应这样清理。
	clear(products[len(result):])
	return result[:len(result):len(result)]
}

// SnapshotCache 只用于同步教学。items 是缓存自己拥有的快照，每次读写都经过复制边界。
// 它没有锁，不声称并发安全；多 goroutine 访问的保护放在 M14。
type SnapshotCache struct {
	items map[string][]Product
}

// NewSnapshotCache 由 cache 实验创建，缓存本次实验独有，不与其他实验共享。
func NewSnapshotCache() *SnapshotCache {
	return &SnapshotCache{items: make(map[string][]Product)}
}

// Put 接收请求方的列表，保存自己的深度有限且适合本 Product 结构的副本。
// c 是缓存对象地址；通过 c.items 写入会改变缓存，不会改变调用者的 products。
func (c *SnapshotCache) Put(key string, products []Product) {
	c.items[key] = CloneProducts(products)
}

// Get 由下一位请求方调用。第二个 bool 区分“缓存没有键”和“有键且快照为 nil/空”。
// 读出后再次复制，避免请求方修改返回值污染下一次读取；成本与只读共享契约应权衡。
func (c *SnapshotCache) Get(key string) ([]Product, bool) {
	products, ok := c.items[key]
	if !ok {
		return nil, false
	}
	return CloneProducts(products), true
}
