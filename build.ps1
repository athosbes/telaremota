# Compila o TelaRemota.
#   .\build.ps1                      -> dist\telaremota.exe (modo direto, sem servidores de terceiros)
#   .\build.ps1 -Server https://...  -> também o modo navegador (servidor + agente legado)
param(
    [string]$Server = "",
    # Versão embutida: por padrão a da tag do git (ex.: 0.5.0, ou 0.5.0-3-gabc123 entre versões)
    [string]$Version = ""
)
$ErrorActionPreference = "Stop"
if (-not $Version) {
    $Version = (git describe --tags --always --dirty 2>$null) -replace '^v', ''
    if (-not $Version) { $Version = "dev" }
}
New-Item -ItemType Directory -Force dist | Out-Null

go build -trimpath -ldflags "-s -w -X main.version=$Version" -o dist/telaremota.exe ./cmd/telaremota
if ($LASTEXITCODE) { exit $LASTEXITCODE }

# Ponto fixo para servidores Linux (ex.: Oracle Cloud): amd64 e arm64
$env:CGO_ENABLED = "0"; $env:GOOS = "linux"
foreach ($arch in "amd64", "arm64") {
    $env:GOARCH = $arch
    go build -trimpath -ldflags "-s -w -X main.version=$Version" -o "dist/telaremota-hub-linux-$arch" ./cmd/telaremota-hub
    if ($LASTEXITCODE) { Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED; exit $LASTEXITCODE }
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED

if ($Server) {
    go build -trimpath -ldflags "-s -w -X main.defaultServer=$Server -X main.version=$Version" -o dist/telaremota-agent.exe ./cmd/agent
    if ($LASTEXITCODE) { exit $LASTEXITCODE }
    $env:GOOS = "linux"; $env:GOARCH = "amd64"
    go build -trimpath -ldflags "-s -w" -o dist/telaremota-server-linux-amd64 ./cmd/server
    $code = $LASTEXITCODE
    Remove-Item Env:GOOS, Env:GOARCH
    if ($code) { exit $code }
}

Get-ChildItem dist | Format-Table Name, @{n = "MB"; e = { "{0:N1}" -f ($_.Length / 1MB) } }
