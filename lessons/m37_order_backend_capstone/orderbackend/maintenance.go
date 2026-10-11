package orderbackend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

// ReadManifest给只读复验入口读取自己的无秘密账本，精确核验所有名称对应同一个run。
// 它不能将任意文件中的表/键收编为owner；本方法不创建、删除或改group offset。
func ReadManifest(path string) (d Manifest, err error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return d, e
	}
	if len(b) > 1<<20 {
		return d, ErrInvalid
	}
	if e = readConfig(path, &d); e != nil {
		return d, e
	}
	return d, d.Validate()
}

// Validate核验精确能力的作用域，ReadManifest/借用/清理均使用同一规则。它证明名字
// 合法，不能证明文件来自可信owner；BorrowCacheKeys的caller必须持原创建者授权，
// 不能将HTTP输入或别人提供的JSON当权限。partial attempt账本可以缺少尚未创建的项。
func (d Manifest) Validate() error {
	if !namespacePattern.MatchString(d.Namespace) || len(d.Tables) > 5 || len(d.Topics) > 1 || len(d.Groups) > 1 || len(d.Keys) > 64 {
		return ErrInvalid
	}
	allowed := map[string]bool{}
	for _, suffix := range TableSuffixes() {
		allowed[d.Namespace+"_"+suffix] = true
	}
	for _, name := range d.Tables {
		if !allowed[name] {
			return ErrInvalid
		}
	}
	run := strings.TrimPrefix(d.Namespace, "m37_")
	topic := "golearn.m37." + run
	for _, name := range d.Topics {
		if name != topic {
			return ErrInvalid
		}
	}
	for _, name := range d.Groups {
		if name != topic+".consumer" {
			return ErrInvalid
		}
	}
	keyPattern := regexp.MustCompile(`^golearn:m37:` + run + `:owner=(alice|bob):order=[a-f0-9]{64}:schema=1:(generation|data)$`)
	for _, key := range d.Keys {
		if !keyPattern.MatchString(key) {
			return ErrInvalid
		}
	}
	for _, names := range [][]string{d.Tables, d.Topics, d.Groups, d.Keys} {
		seen := map[string]bool{}
		for _, name := range names {
			if seen[name] {
				return ErrInvalid
			}
			seen[name] = true
		}
	}
	return nil
}

// BorrowCacheKeys供M38的受信恢复caller在NewApp/Start之前调用。只授予原账本列举键
// 的业务读写借用，不发Redis命令、不添加fresh registry、不取得清理所有权。未列举
// 的已有键仍由Cache.keys拒绝。调用者须先等旧App.Done并关闭旧client，保证一个写者；
// 此能力不是分布式锁，不允许两个仍活跃的relay/consumer同时借同namespace。
func (r *Resources) BorrowCacheKeys(d Manifest) error {
	if r == nil || r.Cache == nil || d.Namespace != r.Namespace {
		return ErrInvalid
	}
	if e := d.Validate(); e != nil {
		return e
	}
	r.Cache.mu.Lock()
	defer r.Cache.mu.Unlock()
	for _, key := range d.Keys {
		r.Cache.known[key] = true
	}
	fmt.Fprintf(r.Out, "CACHE_KEYS_BORROWED namespace=%s keys=%d cleanup_owner_transferred=false\n", r.Namespace, len(d.Keys))
	return nil
}

// VerifyGone由命令维护入口调用，不执行清理，仅对精确账本逐项读真实服务。
// 独立复查可以发现先前DROP/delete返回后仍有对象；不存在结果来自当前协议响应。
func (r *Resources) VerifyGone(ctx context.Context, d Manifest) error {
	if d.Namespace != r.Namespace {
		return ErrInvalid
	}
	if e := d.Validate(); e != nil {
		return e
	}
	for _, name := range d.Tables {
		var n int
		if e := r.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", name).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return fmt.Errorf("精确表仍存在: %s", name)
		}
	}
	for _, key := range d.Keys {
		n, e := r.Cache.client.Exists(ctx, key).Result()
		if e != nil {
			return e
		}
		if n != 0 {
			return errors.New("精确登记Redis键仍存在")
		}
	}
	for _, topic := range d.Topics {
		m, e := r.Broker.Admin.Metadata(kadm.WithAuthorizedOps(ctx), topic)
		if e != nil {
			return e
		}
		t, ok := m.Topics[topic]
		if !ok || !errors.Is(t.Err, kerr.UnknownTopicOrPartition) {
			return errors.New("topic仍存在")
		}
	}
	for _, group := range d.Groups {
		gs, e := r.Broker.Admin.DescribeGroups(ctx, group)
		if e != nil {
			return e
		}
		g, ok := gs[group]
		if !ok || !(errors.Is(g.Err, kerr.GroupIDNotFound) || (g.Err == nil && g.State == "Dead" && len(g.Members) == 0)) {
			return errors.New("group仍存在")
		}
		offsets, e := r.Broker.Admin.FetchOffsets(ctx, group)
		if e != nil {
			return e
		}
		if len(offsets) != 0 {
			return errors.New("group offset仍存在")
		}
	}
	fmt.Fprintf(r.Out, "VERIFY_GONE namespace=%s tables=%d keys=%d topics=%d groups=%d offsets_empty=true\n", d.Namespace, len(d.Tables), len(d.Keys), len(d.Topics), len(d.Groups))
	return nil
}
