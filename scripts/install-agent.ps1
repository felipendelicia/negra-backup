# install-agent.ps1 — instala o actualiza nat-backup-agent en Windows
#
# Uso básico (como Administrador):
#   iwr -useb https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install-agent.ps1 | iex
#
# Con configuración automática:
#   & ([scriptblock]::Create((iwr -useb 'https://.../install-agent.ps1').Content)) -ServerUrl 'http://HOST:8080' -ApiKey 'TU_KEY'

param(
    [string]$ServerUrl = "",
    [string]$ApiKey    = ""
)

$ErrorActionPreference = "Stop"

$Repo       = "felipendelicia/negra-backup"
$InstallDir = "$env:ProgramFiles\nat-backup"
$BinName    = "nat-backup-agent.exe"
$BinPath    = Join-Path $InstallDir $BinName
$AssetName  = "nat-backup-agent-windows-amd64.exe"
$ConfigDir  = "$env:ProgramData\nat-backup"
$ConfigFile = Join-Path $ConfigDir "agent.yaml"

function Write-Ok($msg)   { Write-Host "✓ $msg" -ForegroundColor Green }
function Write-Info($msg) { Write-Host "→ $msg" -ForegroundColor Yellow }
function Write-Err($msg)  { Write-Host "✗ $msg" -ForegroundColor Red; exit 1 }

# ── Última versión ────────────────────────────────────────────────────────────
Write-Info "Obteniendo última versión..."
try {
    $Release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
    $Latest  = $Release.tag_name
} catch {
    Write-Err "No se pudo obtener la última versión: $_"
}
if (-not $Latest) { Write-Err "No se pudo obtener la última versión." }
Write-Info "Última versión: $Latest"

# ── Versión instalada ─────────────────────────────────────────────────────────
if (Test-Path $BinPath) {
    try   { $Current = & $BinPath version 2>$null }
    catch { $Current = "desconocida" }
    if ($Current -eq $Latest) {
        Write-Ok "nat-backup-agent ya está en $Latest, sin cambios."
        exit 0
    }
    Write-Info "Actualizando nat-backup-agent  $Current → $Latest..."
} else {
    Write-Info "Instalando nat-backup-agent $Latest..."
}

# ── Descarga e instalación ────────────────────────────────────────────────────
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$Url = "https://github.com/$Repo/releases/download/$Latest/$AssetName"
$Tmp = Join-Path $env:TEMP $AssetName
try   { Invoke-WebRequest -Uri $Url -OutFile $Tmp }
catch { Write-Err "Error al descargar ${AssetName}: $_" }
Move-Item -Force $Tmp $BinPath
Write-Ok "nat-backup-agent instalado en $BinPath"

# ── PATH del sistema ──────────────────────────────────────────────────────────
$MachinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($MachinePath -notlike "*$InstallDir*") {
    try {
        [Environment]::SetEnvironmentVariable("Path", "$MachinePath;$InstallDir", "Machine")
        Write-Ok "$InstallDir agregado al PATH del sistema."
    } catch {
        Write-Host "  Agrega manualmente al PATH: $InstallDir" -ForegroundColor Yellow
    }
}

# ── Configuración del agente (si se proporcionaron credenciales) ──────────────
if ($ServerUrl -ne "" -and $ApiKey -ne "") {
    New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
    @"
server_url: $ServerUrl
api_key: $ApiKey
"@ | Set-Content -Path $ConfigFile -Encoding UTF8
    Write-Ok "Configuración guardada en $ConfigFile"
}

# ── Resultado ─────────────────────────────────────────────────────────────────
Write-Host ""
Write-Host "nat-backup-agent $Latest instalado correctamente." -ForegroundColor Green
Write-Host ""

if ($ServerUrl -ne "" -and $ApiKey -ne "") {
    Write-Host "Agente configurado. Para iniciar:"
    Write-Host "  nat-backup-agent.exe $ConfigFile"
    Write-Host ""
    Write-Host "O como servicio de Windows (Administrador):"
    Write-Host "  nat-backup-agent.exe install"
} else {
    Write-Host "Próximos pasos:"
    Write-Host "  1. Crear $ConfigFile:"
    Write-Host "       server_url: http://TU_SERVIDOR:8080"
    Write-Host "       api_key:    TU_API_KEY"
    Write-Host "  2. Ejecutar:  nat-backup-agent.exe $ConfigFile"
}
