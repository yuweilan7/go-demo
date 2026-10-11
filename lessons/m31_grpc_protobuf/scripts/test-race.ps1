param([string]$CompilerPath, [string]$CxxPath, [string]$BinDirectory)
$ErrorActionPreference = 'Stop'
$moduleRoot = Split-Path -Parent $PSScriptRoot
$savedCc = $env:CC
$savedCxx = $env:CXX
$savedCgo = $env:CGO_ENABLED
$savedPath = $env:PATH
try {
    # 只给本 PowerShell 进程指定 amd64 编译器；不改 Go 持久配置、不替换默认 MinGW、不改全局 PATH。
    if ($CompilerPath) {
        if (-not (Test-Path -LiteralPath $CompilerPath -PathType Leaf)) { throw "CC 不存在：$CompilerPath" }
        $env:CC = $CompilerPath
        & $CompilerPath --version
        & $CompilerPath -dumpmachine
    }
    if ($CxxPath) {
        if (-not (Test-Path -LiteralPath $CxxPath -PathType Leaf)) { throw "CXX 不存在：$CxxPath" }
        $env:CXX = $CxxPath
    }
    # gcc 会启动同目录的 assembler/linker，并加载工具链 DLL，当前进程需能找到 bin；结束后恢复。
    if (-not $BinDirectory -and $CompilerPath) { $BinDirectory = Split-Path -Parent $CompilerPath }
    if ($BinDirectory) { $env:PATH = "$BinDirectory;$savedPath" }
    $env:CGO_ENABLED = '1'
    go -C $moduleRoot test -race -count=1 -timeout 60s ./...
    if ($LASTEXITCODE -ne 0) { throw "go test -race 失败，退出码 $LASTEXITCODE" }
} finally {
    $env:CC = $savedCc
    $env:CXX = $savedCxx
    $env:CGO_ENABLED = $savedCgo
    $env:PATH = $savedPath
}
