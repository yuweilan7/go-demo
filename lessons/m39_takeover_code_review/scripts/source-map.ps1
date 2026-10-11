$ErrorActionPreference = 'Stop'
$m39Module = Split-Path -Parent $PSScriptRoot
New-Item -ItemType Directory -Path (Join-Path $m39Module '.local/logs') -Force | Out-Null
$m39Gin = go -C $m39Module list -m -f '{{.Dir}}' github.com/gin-gonic/gin
if ($LASTEXITCODE -ne 0) { throw '锁定Gin目录不可读' }
$m39Franz = go -C $m39Module list -m -f '{{.Dir}}' github.com/twmb/franz-go
if ($LASTEXITCODE -ne 0) { throw '锁定franz目录不可读' }
$m39Files = @((Join-Path $m39Gin 'gin.go'),(Join-Path $m39Gin 'context.go'),(Join-Path $m39Gin 'recovery.go'),(Join-Path $m39Franz 'pkg/kgo/consumer_group.go'))
foreach ($m39File in $m39Files) {
    $m39Hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $m39File).Hash
    Write-Output "SOURCE_SHA256 $m39File $m39Hash"
    Select-String -LiteralPath $m39File -Pattern 'func .*ServeHTTP','func .*handleHTTPRequest','func .*Next\(','func .*reset\(','func .*CommitRecords\(','func CustomRecovery','pool.Get','pool.Put','Offset \+ 1' | ForEach-Object { Write-Output ($_.LineNumber.ToString() + ': ' + $_.Line.Trim()) }
}
go version
go -C $m39Module list -m github.com/gin-gonic/gin github.com/go-sql-driver/mysql github.com/redis/go-redis/v9 github.com/twmb/franz-go google.golang.org/grpc gorm.io/gorm
go doc net/http.Request.Context
go doc context.CancelFunc
go doc database/sql.DB.SetMaxOpenConns
