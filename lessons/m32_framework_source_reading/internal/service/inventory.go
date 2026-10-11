// Package service 是 protobuf/框架与库存业务的实际适配，只有这里处理协议错误和生成消息。
package service

import (
	"context"
	"errors"
	kerrors "github.com/go-kratos/kratos/v3/errors"
	pb "go-demo/lessons/m32_framework_source_reading/gen/inventoryv1"
	"go-demo/lessons/m32_framework_source_reading/internal/biz"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"strconv"
)

type Inventory struct {
	pb.UnimplementedInventoryServer
	usecase *biz.Usecase
}

// New 由 root 调用。嵌入生成的 Unimplemented 是向前兼容契约，不是让 framework 自动构造业务依赖。
func New(u *biz.Usecase) *Inventory { return &Inventory{usecase: u} }

// protocolError 由三种 RPC 适配调用。普通 error 不会自动获得业务 status，必须明确分类。
// HTTP 默认 encoder 读取 Kratos/GRPCStatus；gRPC 原样传 Status 及 typed details，不丢 M31 Conflict 契约。
func protocolError(err error) error {
	if err == nil {
		return nil
	}
	var shortage *biz.Insufficient
	if errors.As(err, &shortage) {
		st := status.New(codes.FailedPrecondition, "库存不足")
		detailed, buildErr := st.WithDetails(&pb.Conflict{Sku: shortage.SKU, Requested: shortage.Requested, Available: shortage.Available}, &errdetails.ErrorInfo{Reason: "STOCK_INSUFFICIENT", Metadata: map[string]string{"sku": shortage.SKU, "requested": strconv.Itoa(int(shortage.Requested)), "available": strconv.Itoa(int(shortage.Available))}})
		if buildErr != nil {
			return kerrors.InternalServer("DETAIL_BUILD", "业务详情构造失败").WithCause(buildErr)
		}
		return detailed.Err()
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.Is(err, biz.ErrInvalid):
		return kerrors.BadRequest("INPUT", "库存输入无效").WithCause(err)
	case errors.Is(err, biz.ErrNotFound):
		return kerrors.NotFound("SKU_NOT_FOUND", "SKU 不存在").WithCause(err)
	case errors.Is(err, biz.ErrClosed):
		return kerrors.ServiceUnavailable("CLOSED", "仓储已关闭").WithCause(err)
	default:
		return kerrors.InternalServer("INTERNAL", "库存调用失败").WithCause(err)
	}
}

// Reserve 被 HTTP 生成 handler 或 gRPC 生成 handler 同步调用；req 指针是本请求解码对象。
// 转为 biz.Request 值只带业务字段，避免 usecase 依赖 protobuf、HTTP Context 或连接资源。
func (s *Inventory) Reserve(ctx context.Context, req *pb.ReserveRequest) (*pb.ReserveReply, error) {
	remaining, err := s.usecase.Reserve(ctx, biz.Request{ID: req.GetRequestId(), SKU: req.GetSku(), Quantity: req.GetQuantity(), Mode: int32(req.GetMode())})
	if err != nil {
		return nil, protocolError(err)
	}
	return &pb.ReserveReply{RequestId: req.GetRequestId(), Remaining: remaining}, nil
}

// Inspect 是实际网络查询入口；成功 reply 与仓储快照同时用于断言，HTTP 200 本身不足够。
func (s *Inventory) Inspect(ctx context.Context, req *pb.InspectRequest) (*pb.Stock, error) {
	available, err := s.usecase.Inspect(ctx, req.GetSku())
	if err != nil {
		return nil, protocolError(err)
	}
	return &pb.Stock{Sku: req.GetSku(), Available: available}, nil
}

// Watch 保留 M31 的双向流签名；不创建额外 goroutine，Recv/Send 都在 gRPC 拥有的请求 goroutine 等待。
// 只运行一个小的2条消息半关闭实验；M31的背压/兼容性实验有效证据不在这里重复。
func (s *Inventory) Watch(stream grpc.BidiStreamingServer[pb.WatchRequest, pb.Stock]) error {
	req, err := stream.Recv()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	if req.GetSamples() < 1 || req.GetSamples() > 64 || req.GetPaddingBytes() < 0 || req.GetPaddingBytes() > 256*1024 {
		return kerrors.BadRequest("INPUT", "流样本或padding超界")
	}
	for sequence := int32(1); sequence <= req.GetSamples(); sequence++ {
		reply, err := s.Inspect(stream.Context(), &pb.InspectRequest{Sku: req.GetSku()})
		if err != nil {
			return err
		}
		reply.Sequence = sequence
		reply.Padding = make([]byte, req.GetPaddingBytes())
		if err = stream.Send(reply); err != nil {
			return err
		} // Send 完成只证明传输接受，不证明客户端已经处理。
	}
	_, err = stream.Recv()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return kerrors.BadRequest("INPUT", "本课每流仅允许一个请求")
}
