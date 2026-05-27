# install-agent.ps1 — instala o actualiza nat-backup-agent en Windows
# Uso: iwr -useb https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install-agent.ps1 | iex
# Requiere PowerShell como Administrador para modificar el PATH del sistema.

$ErrorActionPreference = "Stop"

$Repo       = "felipendelicia/negra-backup"
$InstallDir = "$env:ProgramFiles\nat-backup"
$BinName    = "nat-backup-agent.exe"
$BinPath    = Join-Path $InstallDir $BinName
$AssetName  = "nat-backup-agent-windows-amd64.exe"

function Write-Ok($msg)   { Write-Host "✓ $msg" -ForegroundColor Green }
function Write-Info($msg) { Write-Host "→ $msg" -ForegroundColor Yellow }
function Write-Err($msg)  { Write-Host "✗ $msg" -ForegroundColor Red; exit 1 }

# ── Última versión ─────────────────────────────────────────────────────────────
Write-Info "Obteniendo última versión..."
try {
    $Release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
    $Latest  = $Release.tag_name
} catch {
    Write-Err "No se pudo obtener la última versión: $_"
}

if (-not $Latest) { Write-Err "No se pudo obtener la última versión. ¿El repositorio tiene releases publicados?" }
Write-Info "Última versión: $Latest"

# ── Versión instalada ──────────────────────────────────────────────────────────
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

# ── Descarga e instalación ─────────────────────────────────────────────────────
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$Url = "https://github.com/$Repo/releases/download/$Latest/$AssetName"
$Tmp = Join-Path $env:TEMP $AssetName

try {
    Invoke-WebRequest -Uri $Url -OutFile $Tmp
} catch {
    Write-Err "Error al descargar ${AssetName}: $_"
}

Move-Item -Force $Tmp $BinPath
Write-Ok "nat-backup-agent instalado en $BinPath"

# ── PATH del sistema ───────────────────────────────────────────────────────────
$MachinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($MachinePath -notlike "*$InstallDir*") {
    try {
        [Environment]::SetEnvironmentVariable("Path", "$MachinePath;$InstallDir", "Machine")
        Write-Ok "$InstallDir agregado al PATH del sistema."
        Write-Info "Reinicia la terminal para que el PATH tenga efecto."
    } catch {
        Write-Host "  No se pudo modificar el PATH (ejecuta como Administrador para hacerlo automáticamente)." -ForegroundColor Yellow
        Write-Host "  Agrega manualmente al PATH: $InstallDir"
    }
}

Write-Host ""
Write-Host "nat-backup-agent $Latest instalado correctamente." -ForegroundColor Green
Write-Host ""
Write-Host "Próximos pasos:"
Write-Host "  1. Crear agent.yaml con server_url y api_key"
Write-Host "  2. Ejecutar:          nat-backup-agent.exe agent.yaml"
Write-Host "  3. O como servicio:   nat-backup-agent.exe install"
