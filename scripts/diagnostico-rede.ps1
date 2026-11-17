# Diagnostico de rede do TelaRemota.
# Rode nos dois computadores e compare os resultados:
#   powershell -ExecutionPolicy Bypass -File .\diagnostico-rede.ps1
# Consulta servicos publicos: api.ipify.org (IP publico) e servidores STUN do Google/Cloudflare.

$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'
function Titulo($t) { Write-Host "`n== $t ==" -ForegroundColor Cyan }
function Ok($t)     { Write-Host "  [OK] $t" -ForegroundColor Green }
function Ruim($t)   { Write-Host "  [X]  $t" -ForegroundColor Red }
function Aviso($t)  { Write-Host "  [!]  $t" -ForegroundColor Yellow }
function Info($t)   { Write-Host "       $t" }

function Is-CGNAT($ip)   { $o = $ip.Split('.'); [int]$o[0] -eq 100 -and [int]$o[1] -ge 64 -and [int]$o[1] -le 127 }
function Is-Private($ip) { $ip -match '^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)' }

$res = @{}

# ---------------------------------------------------------------- rede local
Titulo "Rede local"
$cfg = Get-NetIPConfiguration | Where-Object { $_.IPv4DefaultGateway -or $_.IPv6DefaultGateway } | Select-Object -First 1
Info "Interface : $($cfg.InterfaceAlias)"
Info "IPv4 local: $($cfg.IPv4Address.IPAddress -join ', ')"
Info "Gateway   : $($cfg.IPv4DefaultGateway.NextHop -join ', ')"

# ---------------------------------------------------------------- IPv4 publico
Titulo "IPv4 publico"
$pub4 = Invoke-RestMethod -Uri 'https://api.ipify.org' -TimeoutSec 6
if ($pub4) { Info "IP publico: $pub4"; $res.pub4 = $pub4 } else { Ruim "nao foi possivel obter (sem IPv4 de saida?)" }

# ---------------------------------------------------------------- CGNAT
Titulo "CGNAT / NAT duplo (rota ate 1.1.1.1)"
$hops = tracert -d -h 6 -w 700 1.1.1.1 |
    Select-Object -Skip 3 |
    ForEach-Object { if ($_ -match '(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\s*$') { $Matches[1] } } |
    Where-Object { $_ -ne '1.1.1.1' }
Info ("Saltos: " + ($hops -join '  ->  '))
$cg   = @($hops | Where-Object { Is-CGNAT $_ })
$priv = @($hops | Select-Object -Skip 1 | Where-Object { Is-Private $_ })
if ($cg.Count) {
    Ruim "CGNAT detectado ($($cg -join ', ')): o IPv4 NAO aceita conexoes de entrada"
    $res.cgnat = $true
} elseif ($priv.Count) {
    Aviso "Ha rede privada depois do roteador ($($priv -join ', ')): NAT duplo, CGNAT ou rede corporativa"
    $res.cgnat = $true
} else {
    Ok "Sem CGNAT aparente: o IPv4 publico chega ate o seu roteador"
    $res.cgnat = $false
}

# ---------------------------------------------------------------- IPv6
Titulo "IPv6"
$v6 = Get-NetIPAddress -AddressFamily IPv6 -AddressState Preferred | Where-Object { $_.IPAddress -match '^[23]' }
if ($v6) {
    $v6 | ForEach-Object { Info "$($_.IPAddress)  ($($_.InterfaceAlias))" }
    $pub6 = Invoke-RestMethod -Uri 'https://api6.ipify.org' -TimeoutSec 6
    if ($pub6) { Ok "IPv6 funcionando na internet (saida: $pub6)"; $res.ipv6 = $true }
    else       { Aviso "Tem endereco IPv6 global, mas nao saiu para a internet" }
} else {
    Ruim "Nenhum endereco IPv6 global"
}

# ---------------------------------------------------------------- UPnP
Titulo "UPnP no roteador (pode levar ~10 s)"
$finder = New-Object -ComObject UPnP.UPnPDeviceFinder
$igd = $null
foreach ($v in 1, 2) {
    $d = $finder.FindByType("urn:schemas-upnp-org:device:InternetGatewayDevice:$v", 0)
    if ($d -and $d.Count -gt 0) { $igd = $d; break }
}
if ($igd) {
    foreach ($dev in $igd) { Ok "Roteador com UPnP: $($dev.FriendlyName) ($($dev.ModelName))" }
    $res.upnp = $true
} else {
    Ruim "Nenhum roteador com UPnP encontrado (desligado ou bloqueado)"
}

# ---------------------------------------------------------------- tipo de NAT (STUN)
Titulo "Tipo de NAT (STUN, UDP)"
function Stun($udp, $server, $port) {
    $ip = [Net.Dns]::GetHostAddresses($server) | Where-Object AddressFamily -eq 'InterNetwork' | Select-Object -First 1
    if (-not $ip) { return $null }
    $tx = New-Object byte[] 12; (New-Object Random).NextBytes($tx)
    $req = [byte[]](0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xA4, 0x42) + $tx
    [void]$udp.Send($req, $req.Length, (New-Object Net.IPEndPoint($ip, $port)))
    $from = New-Object Net.IPEndPoint([Net.IPAddress]::Any, 0)
    try { $r = $udp.Receive([ref]$from) } catch { return $null }
    $i = 20
    while ($i + 4 -le $r.Length) {
        $type = ([int]$r[$i] -shl 8) -bor $r[$i + 1]
        $len  = ([int]$r[$i + 2] -shl 8) -bor $r[$i + 3]
        if ($type -eq 0x0020 -and $r[$i + 5] -eq 1) {   # XOR-MAPPED-ADDRESS, IPv4
            $p = ((([int]$r[$i + 6] -shl 8) -bor $r[$i + 7]) -bxor 0x2112)
            $a = '{0}.{1}.{2}.{3}' -f ($r[$i + 8] -bxor 0x21), ($r[$i + 9] -bxor 0x12), ($r[$i + 10] -bxor 0xA4), ($r[$i + 11] -bxor 0x42)
            return "${a}:$p"
        }
        $i += 4 + [math]::Ceiling($len / 4) * 4
    }
    return $null
}
$udp = New-Object Net.Sockets.UdpClient(0)
$udp.Client.ReceiveTimeout = 2500
$m1 = Stun $udp 'stun.l.google.com' 19302
$m2 = Stun $udp 'stun.cloudflare.com' 3478
$udp.Close()
Info "Porta local UDP mapeada como -> Google: $m1   Cloudflare: $m2"
if (-not $m1 -and -not $m2) {
    Ruim "UDP de saida bloqueado (comum em rede corporativa): P2P impossivel, so via relay TCP/443"
    $res.nat = 'bloqueado'
} elseif ($m1 -and $m2 -and $m1 -eq $m2) {
    Ok "NAT 'cone' (mesmo mapeamento para destinos diferentes): furo de NAT funciona"
    $res.nat = 'cone'
} elseif ($m1 -and $m2) {
    Ruim "NAT simetrico (mapeamento muda por destino): furo de NAT tende a falhar"
    $res.nat = 'simetrico'
} else {
    Aviso "So um servidor STUN respondeu; resultado inconclusivo"
}

# ---------------------------------------------------------------- resumo
Titulo "RESUMO - este computador pode RECEBER conexao direta?"
$caminhos = @()
if ($res.ipv6) { $caminhos += "IPv6: sim (liberar a porta UDP no firewall IPv6 do roteador)" }
if ($res.pub4 -and -not $res.cgnat) {
    if ($res.upnp) { $caminhos += "IPv4: sim, automatico via UPnP" }
    else           { $caminhos += "IPv4: sim, com redirecionamento de porta manual no roteador" }
}
if ($caminhos.Count) { $caminhos | ForEach-Object { Ok $_ } }
else { Ruim "Nao recebe conexao direta. Opcoes: furo de NAT (se o outro lado puder), ou relay no seu VPS" }
if ($res.nat -eq 'cone') { Info "Furo de NAT (troca de codigos): deve funcionar a partir daqui." }
Write-Host ""
