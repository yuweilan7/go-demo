param([switch]$Integration)
$ErrorActionPreference = 'Stop'
# 只读取主agent已提供的便携工具链，不安装或改全局go env/PATH。
$record = Get-Content -LiteralPath 'D:/IdeaProject/go-demo/.local/course-orchestrator/toolchain.json' -Raw | ConvertFrom-Json
$module = Split-Path -Parent $PSScriptRoot
$priorCC = $env:CC
$priorCXX = $env:CXX
$priorCGO = $env:CGO_ENABLED
$priorPath = $env:PATH
$priorIntegration = $env:GOLEARN_KAFKA_INTEGRATION
try {
    $env:CC = $record.cc
    $env:CXX = $record.cxx
    $env:CGO_ENABLED = '1'
    $env:PATH = $record.bin_dir + ';' + $priorPath
    if ($Integration) { $env:GOLEARN_KAFKA_INTEGRATION = '1' }
    go -C $module test -race -count=1 -timeout 3m ./...
    if ($LASTEXITCODE -ne 0) { throw "race失败，go退出码=$LASTEXITCODE" }
} finally {
    $env:CC = $priorCC
    $env:CXX = $priorCXX
    $env:CGO_ENABLED = $priorCGO
    $env:PATH = $priorPath
    $env:GOLEARN_KAFKA_INTEGRATION = $priorIntegration
}
