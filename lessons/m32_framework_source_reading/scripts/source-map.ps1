$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot

# 只读锁定版官方源码，不把Kitex/go-zero加入本课runtime依赖，也不编译/运行它们。
# Go下载写共享module cache属于正常依赖缓存；所有报告只由调用方保存到本模块。
function Read-PinnedModule([string]$Name, [string]$Version) {
    $json = go -C $moduleRoot mod download -json "$Name@$Version"
    if ($LASTEXITCODE -ne 0) { throw "源码下载失败: $Name@$Version" }
    $metadata = $json | ConvertFrom-Json
    Write-Output "MODULE $($metadata.Path) $($metadata.Version) sum=$($metadata.Sum)"
    return $metadata.Dir
}

function Show-Symbols([string]$SourceRoot, [string[]]$Files, [string]$Pattern) {
    foreach ($file in $Files) {
        $path = Join-Path $SourceRoot $file
        Write-Output "FILE $file SHA256=$((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash)"
        $lines = Get-Content -LiteralPath $path
        for ($index = 0; $index -lt $lines.Count; $index++) {
            if ($lines[$index] -match $Pattern) { Write-Output ("{0}:{1}: {2}" -f $file, ($index + 1), $lines[$index].Trim()) }
        }
    }
}

$kratosResult = @(Read-PinnedModule 'github.com/go-kratos/kratos/v3' 'v3.0.0')
$kratosResult[0]; $kratosRoot = $kratosResult[-1]
Show-Symbols $kratosRoot @('app.go','options.go','middleware/middleware.go','middleware/recovery/recovery.go','transport/http/server.go','transport/http/client.go','transport/http/context.go','transport/http/router.go','transport/http/codec.go','transport/grpc/server.go','transport/grpc/client.go','transport/grpc/interceptor.go','internal/context/context.go','errors/errors.go','transport/http/status/status.go','config/config.go') '^func |^type (Handler|Middleware|Option)|WithTimeout|WithoutCancel|WithCancel|Shutdown\(|GracefulStop\(|errors.FromError|res.Body.Close|server.Start|server.Stop|WithDetails'

$kitexResult = @(Read-PinnedModule 'github.com/cloudwego/kitex' 'v0.16.4')
$kitexResult[0]; $kitexRoot = $kitexResult[-1]
Show-Symbols $kitexRoot @('client/client.go','client/option.go','server/server.go','server/option.go','pkg/remote/remotesvr/server.go') '^func .*?(NewClient|Call\(|Close\(|NewServer|Run\(|Stop\(|buildInvokeChain|buildMiddlewares|RegisterService|WithRPCTimeout|WithFailureRetry|WithEnableContextTimeout)|WithRetryIfNeeded|GracefulStop|Deregister|serverTimeoutMW'

$zeroResult = @(Read-PinnedModule 'github.com/zeromicro/go-zero' 'v1.10.3')
$zeroResult[0]; $zeroRoot = $zeroResult[-1]
Show-Symbols $zeroRoot @('zrpc/client.go','zrpc/server.go','zrpc/internal/rpcserver.go','zrpc/internal/serverinterceptors/timeoutinterceptor.go','core/proc/shutdown.go') '^func .*?(NewClient|NewServer|Start\(|Stop\(|setupUnaryInterceptors|UnaryTimeoutInterceptor|AddShutdownListener|gracefulStop)|GracefulStop|logx.Close|ctx.Done|go func|handler\(ctx|waitForCalled'
