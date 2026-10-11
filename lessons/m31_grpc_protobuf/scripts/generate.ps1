param([switch]$Install)
$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
$privateRoot = Join-Path $moduleRoot '.local'
$toolBin = Join-Path $privateRoot 'bin'
$protocRoot = Join-Path $privateRoot 'protoc-36.2'

# 工具只落在本模块忽略目录；GOBIN/PATH 是本 PowerShell 进程的临时环境，finally 恢复。
if ($Install) {
    New-Item -ItemType Directory -Path $toolBin -Force | Out-Null
    $archive = Join-Path $privateRoot 'protoc-36.2-win64.zip'
    if (-not (Test-Path -LiteralPath (Join-Path $protocRoot 'bin/protoc.exe'))) {
        Invoke-WebRequest -Uri 'https://github.com/protocolbuffers/protobuf/releases/download/v36.2/protoc-36.2-win64.zip' -OutFile $archive
        Expand-Archive -LiteralPath $archive -DestinationPath $protocRoot -Force
    }
    $savedGobin = $env:GOBIN
    try {
        $env:GOBIN = $toolBin
        go -C $moduleRoot install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
        if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-go 安装失败' }
        go -C $moduleRoot install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
        if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-go-grpc 安装失败' }
    } finally { $env:GOBIN = $savedGobin }
}

$protoc = Join-Path $protocRoot 'bin/protoc.exe'
$goPlugin = Join-Path $toolBin 'protoc-gen-go.exe'
$grpcPlugin = Join-Path $toolBin 'protoc-gen-go-grpc.exe'
foreach ($tool in @($protoc, $goPlugin, $grpcPlugin)) {
    if (-not (Test-Path -LiteralPath $tool)) { throw "工具不存在：$tool；先执行 -Install" }
}
& $protoc --version
& $goPlugin --version
& $grpcPlugin --version
$savedPath = $env:PATH
try {
    $env:PATH = "$toolBin;$savedPath"
    # module= 去掉 go_package 的模块前缀，让输出稳定落在 gen/，无需全局 go.work。
    & $protoc "--proto_path=$moduleRoot/proto" "--go_out=$moduleRoot" '--go_opt=module=go-demo/lessons/m31_grpc_protobuf' "--go-grpc_out=$moduleRoot" '--go-grpc_opt=module=go-demo/lessons/m31_grpc_protobuf' 'inventory/v1/inventory.proto' 'compat/v1/record.proto' 'compat/v2/record.proto'
    if ($LASTEXITCODE -ne 0) { throw 'protoc 生成失败' }
} finally { $env:PATH = $savedPath }
