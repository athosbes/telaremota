# Teste ponta a ponta da rede Tailscale embutida (tsnet), num unico computador.
#
# Sobe dois nos Tailscale embutidos -- "tr-empresa" (ponto fixo + agente) e
# "tr-casa" (agente) -- em diretorios temporarios, e testa pela rede Tailscale:
# pareamento, lista de computadores e sessoes de imagem nos dois sentidos.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\teste-tailnet.ps1
#
# Pede uma auth key REUTILIZAVEL (login.tailscale.com -> Settings -> Keys).
# A chave e digitada de forma oculta e fica so nos diretorios temporarios,
# apagados no final. Os nos sao efemeros: somem do painel do Tailscale pouco
# depois do teste.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
function Titulo($t) { Write-Host "`n== $t ==" -ForegroundColor Cyan }
function Ok($t)     { Write-Host "  [OK] $t" -ForegroundColor Green }
function Ruim($t)   { Write-Host "  [X]  $t" -ForegroundColor Red }
function Info($t)   { Write-Host "       $t" }

$repo = Split-Path -Parent $PSScriptRoot
$base = Join-Path $env:TEMP "telaremota-teste-tailnet"
$exe  = Join-Path $base "telaremota.exe"
$port = 47090
$procs = @{}
$falhas = 0

# ------------------------------------------------------------ ambiente por no
$nos = @{
    empresa = @{ Maq = "$base\empresa"; Usr = "$base\empresa-u"; Relay = 47091 }
    casa    = @{ Maq = "$base\casa";    Usr = "$base\casa-u";    Relay = 47092 }
}
function Use-No($n) {
    $env:TELAREMOTA_MAQUINA = $nos[$n].Maq
    $env:TELAREMOTA_USUARIO = $nos[$n].Usr
    $env:TELAREMOTA_RELAY_PORT = "$($nos[$n].Relay)"
    $env:TELAREMOTA_TAILNET_EFEMERO = "1"
}
function Start-No($n, [string[]]$args_) {
    Use-No $n
    $p = Start-Process -FilePath $exe -ArgumentList $args_ -WindowStyle Hidden -PassThru `
        -RedirectStandardError "$base\$n.log" -RedirectStandardOutput "$base\$n.out"
    $procs[$n] = $p
}
function Run-No($n, [string[]]$args_) {
    Use-No $n
    $ErrorActionPreference = 'Continue' # stderr do executavel nao e erro do script
    $out = & $exe @args_ 2>&1 | ForEach-Object { "$_" } | Out-String
    return @{ Code = $LASTEXITCODE; Out = $out.Trim() }
}
function Wait-Tailnet($n, $secs) {
    $f = "$($nos[$n].Maq)\tailnet.json"
    $fim = (Get-Date).AddSeconds($secs)
    while ((Get-Date) -lt $fim) {
        if (Test-Path $f) {
            try { $st = Get-Content $f -Raw | ConvertFrom-Json } catch { $st = $null }
            if ($st -and $st.state -eq 'Running' -and $st.ip) { return $st }
            if ($st -and $st.auth_url) { throw "o no '$n' pede login ($($st.auth_url)) -- a chave foi recusada?" }
        }
        if ($procs[$n].HasExited) { throw "o processo '$n' terminou. Log:`n$(Get-Content "$base\$n.log" -Raw)" }
        Start-Sleep -Milliseconds 500
    }
    throw "tempo esgotado esperando a rede Tailscale em '$n'"
}
function Wait-Log($n, $pattern, $secs) {
    $fim = (Get-Date).AddSeconds($secs)
    while ((Get-Date) -lt $fim) {
        $t = Get-Content "$base\$n.log" -Raw -Encoding UTF8 -ErrorAction SilentlyContinue
        if ($t -match $pattern) { return $Matches[0] }
        Start-Sleep -Milliseconds 500
    }
    return $null
}
function Stop-Todos {
    foreach ($p in $procs.Values) { if ($p -and -not $p.HasExited) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } }
    Start-Sleep -Milliseconds 500
}

try {
    # -------------------------------------------------------- preparo
    Titulo "Preparo"
    if (Test-Path $base) { Remove-Item -Recurse -Force $base }
    New-Item -ItemType Directory -Force $base | Out-Null
    Push-Location $repo
    go build -o $exe ./cmd/telaremota
    $buildOk = ($LASTEXITCODE -eq 0)
    Pop-Location
    if (-not $buildOk) { throw "falha ao compilar" }
    Ok "compilado em $exe"

    $sec = Read-Host -AsSecureString "  Cole a auth key do Tailscale (tskey-auth-...; nao aparece na tela)"
    $key = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
    $key = $key.Trim()
    if (-not $key.StartsWith("tskey-")) { throw "chave invalida (deve comecar com tskey-)" }
    foreach ($n in $nos.Keys) {
        New-Item -ItemType Directory -Force $nos[$n].Maq | Out-Null
        [IO.File]::WriteAllText("$($nos[$n].Maq)\tailnet-authkey", $key)
    }
    $key = $null; $sec = $null
    # O ponto fixo de teste precisa de machine.json para gerar convites.
    $m = @{ hub = $true; port = $port; upnp = $false; name = "tr-empresa"; agent = $true; tailnet = $true } | ConvertTo-Json
    [IO.File]::WriteAllText("$($nos.empresa.Maq)\machine.json", $m)

    # -------------------------------------------------------- nos
    Titulo "Subindo os dois nos Tailscale embutidos"
    Info "(se o Windows perguntar sobre o firewall, pode permitir ou cancelar: o teste funciona nos dois casos)"
    Start-No empresa @("ponto-fixo", "-tailnet", "-agente", "-sem-upnp", "-porta", "$port", "-nome", "tr-empresa")
    Start-No casa    @("agente", "-tailnet", "-nome", "tr-casa")
    $stE = Wait-Tailnet empresa 90
    Ok "tr-empresa conectado: $($stE.ip)  ($($stE.dns_name))"
    $stC = Wait-Tailnet casa 90
    Ok "tr-casa conectado:    $($stC.ip)  ($($stC.dns_name))"
    foreach ($n in $nos.Keys) {
        if (Test-Path "$($nos[$n].Maq)\tailnet-authkey") { Ruim "chave ainda no disco em '$n'"; $falhas++ } else { Ok "chave apagada do disco em '$n' apos o login" }
    }
    if (-not (Wait-Log empresa 'tamb\S+m ouvindo em \S+' 30)) { throw "o ponto fixo nao abriu a porta na rede Tailscale. Log:`n$(Get-Content "$base\empresa.log" -Raw)" }
    Ok "ponto fixo ouvindo em $($stE.ip):$port na rede Tailscale"

    # -------------------------------------------------------- pareamento
    Titulo "Pareamento (casa -> empresa pela rede Tailscale)"
    $r = Run-No empresa @("convite", "-usos", "2")
    if ($r.Out -notmatch '(?<![\w-])([a-z0-9]{3}-[a-z0-9]{3}-[a-z0-9]{3})(?![\w-])') { throw "convite sem codigo:`n$($r.Out)" }
    $code = $Matches[1]
    $addr = "$($stE.ip):$port"
    if ($r.Out -notmatch [regex]::Escape($stE.ip)) { Ruim "o convite nao sugeriu o endereco Tailscale ($($stE.ip))"; $falhas++ } else { Ok "convite aponta para o endereco Tailscale $addr" }
    $t0 = Get-Date
    $r = Run-No casa @("parear", $addr, $code, "-nome", "tr-casa")
    if ($r.Code -ne 0) { throw "pareamento falhou:`n$($r.Out)" }
    Ok ("pareado em {0:N1}s" -f ((Get-Date) - $t0).TotalSeconds)
    $r.Out -split "`n" | ForEach-Object { Info $_.Trim() }

    # O agente da casa subiu antes do pareamento: reinicia para ele se registrar.
    Stop-Process -Id $procs.casa.Id -Force; Start-Sleep 1
    Start-No casa @("agente", "-tailnet", "-nome", "tr-casa")
    Wait-Tailnet casa 60 | Out-Null
    if (-not (Wait-Log casa 'online como' 45)) { throw "agente da casa nao ficou online. Log:`n$(Get-Content "$base\casa.log" -Raw)" }
    $online = Wait-Log empresa 'agente "tr-casa" online \([^)]+\)' 10
    Ok "agente da casa registrado no ponto fixo: $online"
    if ($online -notmatch '\(100\.') { Ruim "o agente da casa nao chegou pela rede Tailscale"; $falhas++ }

    # -------------------------------------------------------- uso
    Titulo "Lista de computadores (usuario da casa)"
    $r = Run-No casa @("lista")
    $r.Out -split "`n" | ForEach-Object { Info $_.Trim() }
    if ($r.Code -eq 0 -and $r.Out -match 'tr-empresa' -and $r.Out -match 'tr-casa') { Ok "lista ok" } else { Ruim "lista incompleta"; $falhas++ }

    Titulo "Sessao de imagem: casa -> tr-empresa (8 s)"
    $r = Run-No casa @("conectar", "tr-empresa", "-diag", "8s")
    Info $r.Out
    if ($r.Code -eq 0) { Ok "sessao ok" } else { Ruim "sessao falhou"; $falhas++ }

    Titulo "Sessao de imagem: casa -> tr-casa, ida e volta pelo ponto fixo (5 s)"
    $r = Run-No casa @("conectar", "tr-casa", "-diag", "5s")
    Info $r.Out
    if ($r.Code -eq 0) { Ok "sessao ok" } else { Ruim "sessao falhou"; $falhas++ }
}
catch {
    Ruim "$_"
    $falhas++
}
finally {
    Stop-Todos
    Titulo "Resultado"
    if ($falhas -eq 0) { Ok "tudo certo" } else { Ruim "$falhas falha(s)" }
    Copy-Item "$base\empresa.log" "$env:TEMP\telaremota-teste-empresa.log" -ErrorAction SilentlyContinue
    Copy-Item "$base\casa.log" "$env:TEMP\telaremota-teste-casa.log" -ErrorAction SilentlyContinue
    Info "logs: $env:TEMP\telaremota-teste-empresa.log e telaremota-teste-casa.log"
    Remove-Item -Recurse -Force $base -ErrorAction SilentlyContinue
    foreach ($v in 'TELAREMOTA_MAQUINA', 'TELAREMOTA_USUARIO', 'TELAREMOTA_RELAY_PORT', 'TELAREMOTA_TAILNET_EFEMERO') { Remove-Item "Env:$v" -ErrorAction SilentlyContinue }
}
