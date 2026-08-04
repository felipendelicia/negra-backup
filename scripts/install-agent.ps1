# install-agent.ps1 - installs or updates nat-backup-agent on Windows.
#
# Basic usage (as Administrator):
#   iwr -useb https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install-agent.ps1 | iex
#
# With automatic configuration:
#   & ([scriptblock]::Create((iwr -useb 'https://.../install-agent.ps1').Content)) -ServerUrl 'http://HOST:8080' -ApiKey 'YOUR_KEY'
#
# The API key can also be supplied through the NAT_BACKUP_API_KEY environment
# variable so it never shows up in the command line or the shell history.
#
# -Force lets the installer stop a running agent (service or bare process) so
# the binary can be replaced; without it the script refuses to touch a busy
# executable and tells you what to stop.

param(
    [string]$ServerUrl = "",
    [string]$ApiKey    = "",
    [switch]$Force
)

$ErrorActionPreference = "Stop"
$ProgressPreference    = "SilentlyContinue"   # Invoke-WebRequest is far faster without it

$Repo        = "felipendelicia/negra-backup"
$InstallDir  = Join-Path $env:ProgramFiles "nat-backup"
$BinName     = "nat-backup-agent.exe"
$BinPath     = Join-Path $InstallDir $BinName
$ConfigDir   = Join-Path $env:ProgramData "nat-backup"
$ConfigFile  = Join-Path $ConfigDir "agent.yaml"
$ServiceName = "nat-backup-agent"

# The release workflow (.github/workflows/release.yml) builds the agent for
# windows/amd64 only. Keep this in sync with that file.
$AssetName = "nat-backup-agent-windows-amd64.exe"

# Candidate names for the release checksum manifest. The release does not
# publish one today; if any of these exists the download is verified against it.
$ChecksumAssets = @("SHA256SUMS", "checksums.txt", "SHA256SUMS.txt")

# Plain ASCII markers: Windows PowerShell 5.1 reads unsigned scripts using the
# ANSI code page, so non-ASCII output characters come out as mojibake.
function Write-Ok   ($msg) { Write-Host "OK    $msg" -ForegroundColor Green }
function Write-Info ($msg) { Write-Host "  ->  $msg" -ForegroundColor Yellow }
function Write-Warn ($msg) { Write-Host "WARN  $msg" -ForegroundColor Yellow }
function Fail       ($msg) { throw $msg }

function Test-Administrator {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

# Restricts a file or directory to SYSTEM and the local Administrators group,
# dropping every inherited entry. This is the Windows equivalent of the 0600
# root-owned config the Linux installer writes: without it agent.yaml inherits
# the ProgramData ACL, where BUILTIN\Users can read the API key.
# SIDs are used instead of names so it also works on non-English Windows.
function Protect-Path {
    param([Parameter(Mandatory = $true)][string]$Path)

    $isDir  = (Get-Item -LiteralPath $Path).PSIsContainer
    $system = New-Object Security.Principal.SecurityIdentifier('S-1-5-18')      # NT AUTHORITY\SYSTEM
    $admins = New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')  # BUILTIN\Administrators

    $acl = Get-Acl -LiteralPath $Path
    $acl.SetAccessRuleProtection($true, $false)   # break inheritance, discard inherited ACEs
    foreach ($rule in @($acl.Access)) { [void]$acl.RemoveAccessRule($rule) }

    foreach ($sid in @($system, $admins)) {
        if ($isDir) {
            $rule = New-Object Security.AccessControl.FileSystemAccessRule(
                $sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        } else {
            $rule = New-Object Security.AccessControl.FileSystemAccessRule(
                $sid, 'FullControl', 'Allow')
        }
        $acl.AddAccessRule($rule)
    }

    try { $acl.SetOwner($admins) } catch { }   # not fatal if the privilege is missing
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Get-AgentVersion {
    param([string]$Exe)
    if (-not (Test-Path -LiteralPath $Exe)) { return $null }
    try {
        $out = & $Exe version 2>$null | Select-Object -First 1
        if ($LASTEXITCODE -ne 0) { return $null }
        if ($out) { return $out.ToString().Trim() }
        return $null
    } catch { return $null }
}

function Get-AgentProcess {
    param([string]$Exe)
    $name = [IO.Path]::GetFileNameWithoutExtension($Exe)
    Get-Process -Name $name -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -eq $Exe } catch { $false }
    }
}

# Arguments a running agent was launched with, so an update can restart it the
# same way instead of guessing a config path.
function Get-AgentArgumentLine {
    param([string]$Exe)
    try {
        $name = [IO.Path]::GetFileName($Exe)
        $procs = @(Get-CimInstance -ClassName Win32_Process -Filter "Name='$name'" -ErrorAction Stop |
                   Where-Object { $_.ExecutablePath -eq $Exe })
        if ($procs.Count -eq 0) { return $null }
        $cl = $procs[0].CommandLine
        if ([string]::IsNullOrWhiteSpace($cl)) { return $null }
        $cl = $cl.Trim()
        if ($cl.StartsWith('"')) {
            $end = $cl.IndexOf('"', 1)
            if ($end -lt 0) { return $null }
            return $cl.Substring($end + 1).Trim()
        }
        $sp = $cl.IndexOf(' ')
        if ($sp -lt 0) { return "" }
        return $cl.Substring($sp + 1).Trim()
    } catch { return $null }
}

function Stop-AgentService {
    param([string]$Name, $Service)
    $Service.Refresh()
    if ($Service.Status -eq 'Stopped') { return }
    Write-Info "Stopping the $Name service..."
    Stop-Service -Name $Name -Force
    try {
        $Service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(60))
    } catch {
        Fail "The $Name service did not stop within 60 s; the agent cannot be updated while it is running."
    }
}

function Stop-BareAgent {
    param($Processes)
    Write-Info "Stopping the running nat-backup-agent (PID $($Processes.Id -join ', '))..."
    $Processes | Stop-Process -Force
    foreach ($proc in $Processes) {
        try { $proc.WaitForExit(30000) | Out-Null } catch { }
    }
}

function Start-BareAgent {
    param([string]$Exe, [string]$Arguments)
    Write-Info "Restarting nat-backup-agent $Arguments..."
    Start-Process -FilePath $Exe -ArgumentList $Arguments -WindowStyle Hidden | Out-Null
    Start-Sleep -Seconds 3
    if (@(Get-AgentProcess -Exe $Exe).Count -eq 0) {
        Fail "nat-backup-agent did not stay running after the update. Run it in the foreground to see why: & `"$Exe`" $Arguments"
    }
    Write-Ok "nat-backup-agent restarted (version $(Get-AgentVersion -Exe $Exe))"
    Write-Warn "It is running as a bare process with no supervisor: it will not come back after a reboot. Consider wrapping it in a Windows service (NSSM or WinSW)."
}

function Wait-ServiceRunning {
    param([string]$Name)
    $svc = Get-Service -Name $Name
    try {
        $svc.WaitForStatus('Running', [TimeSpan]::FromSeconds(60))
    } catch {
        Fail "The $Name service did not reach the Running state. Inspect it with: sc.exe query $Name  /  Get-EventLog -LogName Application -Newest 20"
    }
    # A crash loop shows up as Running and then Stopped a moment later.
    Start-Sleep -Seconds 3
    $svc.Refresh()
    if ($svc.Status -ne 'Running') {
        Fail "The $Name service started and stopped again (state: $($svc.Status)). Check $ConfigFile and the Windows Event Log."
    }
}

function Invoke-Install {
    if (-not (Test-Administrator)) {
        Fail "Administrator privileges are required to write to $InstallDir and to restrict access to the API key. Re-run this script from an elevated PowerShell."
    }

    # -- Platform ------------------------------------------------------------
    $procArch = $env:PROCESSOR_ARCHITECTURE
    if ($procArch -eq 'x86' -and -not $env:PROCESSOR_ARCHITEW6432) {
        Fail "This host is 32-bit ($procArch). The release only publishes a 64-bit agent ($AssetName)."
    }
    if ($procArch -eq 'ARM64') {
        Write-Warn "This host is ARM64 and the release only publishes an amd64 agent; it will run through Windows emulation."
    }

    # GitHub requires TLS 1.2+; Windows PowerShell 5.1 still defaults to TLS 1.0.
    try {
        [Net.ServicePointManager]::SecurityProtocol =
            [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch { }

    # -- Arguments -----------------------------------------------------------
    $key = $ApiKey
    if ([string]::IsNullOrWhiteSpace($key)) { $key = $env:NAT_BACKUP_API_KEY }
    if ($null -eq $key) { $key = "" }
    $url = $ServerUrl

    $configureAgent = $false
    if (-not [string]::IsNullOrWhiteSpace($url) -or -not [string]::IsNullOrWhiteSpace($key)) {
        if ([string]::IsNullOrWhiteSpace($url)) {
            Fail "An API key was given without -ServerUrl. Both are required to configure the agent."
        }
        if ([string]::IsNullOrWhiteSpace($key)) {
            Fail "-ServerUrl was given without an API key. Both are required to configure the agent."
        }
        if ($url -notmatch '^https?://') {
            Fail "-ServerUrl must start with http:// or https:// (got: $url)"
        }
        if ($url -match '\s') { Fail "-ServerUrl must not contain whitespace." }
        # The server issues 32-byte hex keys; keep the accepted charset tight so
        # the value cannot break out of the YAML string it is written into.
        if ($key -notmatch '^[A-Za-z0-9._~+/=-]+$') {
            Fail "The API key contains unsupported characters. Expected only letters, digits and . _ ~ + / = -"
        }
        $configureAgent = $true
    }

    # -- Latest release ------------------------------------------------------
    Write-Info "Fetching the latest release..."
    try {
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
        $latest  = $release.tag_name
    } catch {
        Fail "Could not query the GitHub releases API for ${Repo}: $($_.Exception.Message)"
    }
    if ([string]::IsNullOrWhiteSpace($latest)) {
        Fail "The GitHub API returned no version tag. Does $Repo have any published release?"
    }
    if ($latest -notmatch '^[A-Za-z0-9._-]+$') {
        Fail "Unexpected release tag received from GitHub: '$latest'."
    }
    Write-Info "Latest version: $latest"

    $baseUrl = "https://github.com/$Repo/releases/download/$latest"
    $tmpDir  = Join-Path $env:TEMP ("nat-backup-install-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

    try {
        $restartNeeded  = $false
        $stoppedProcess = $false
        $service        = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

        # -- Binary ----------------------------------------------------------
        $current = Get-AgentVersion -Exe $BinPath
        if ($current -eq $latest) {
            Write-Ok "nat-backup-agent is already at $latest, nothing to do."
        } else {
            if ($null -eq $current) {
                Write-Info "Installing nat-backup-agent $latest..."
            } else {
                Write-Info "Updating nat-backup-agent  $current -> $latest..."
            }

            $tmpExe = Join-Path $tmpDir $AssetName
            try {
                Invoke-WebRequest -Uri "$baseUrl/$AssetName" -OutFile $tmpExe -UseBasicParsing
            } catch {
                Fail "Failed to download $AssetName from ${baseUrl}: $($_.Exception.Message). Does release $latest publish that asset?"
            }
            if (-not (Test-Path -LiteralPath $tmpExe) -or (Get-Item -LiteralPath $tmpExe).Length -eq 0) {
                Fail "$AssetName downloaded empty from $baseUrl."
            }
            $stream = [System.IO.File]::OpenRead($tmpExe)
            try     { $b0 = $stream.ReadByte(); $b1 = $stream.ReadByte() }
            finally { $stream.Dispose() }
            if ($b0 -ne 0x4D -or $b1 -ne 0x5A) {   # "MZ"
                Fail "$AssetName is not a Windows executable (bad MZ header). The download is corrupt or the release asset is wrong."
            }

            # Checksum manifest, when the release publishes one.
            $checksums = $null
            foreach ($asset in $ChecksumAssets) {
                $dest = Join-Path $tmpDir $asset
                try {
                    Invoke-WebRequest -Uri "$baseUrl/$asset" -OutFile $dest -UseBasicParsing -ErrorAction Stop
                    if ((Test-Path -LiteralPath $dest) -and (Get-Item -LiteralPath $dest).Length -gt 0) {
                        $checksums = $dest
                        Write-Ok "Checksum manifest found in the release: $asset"
                        break
                    }
                } catch { }
            }
            if ($null -eq $checksums) {
                Write-Warn "Release $latest publishes no checksum manifest ($($ChecksumAssets -join ', ')): the download cannot be verified against a published hash. Falling back to transport and integrity checks (HTTPS, HTTP status, non-empty PE executable that runs)."
            } else {
                $expected = $null
                foreach ($line in (Get-Content -LiteralPath $checksums)) {
                    $parts = $line -split '\s+', 2
                    if ($parts.Count -eq 2 -and $parts[1].TrimStart('*').Trim() -eq $AssetName) {
                        $expected = $parts[0].Trim().ToLowerInvariant()
                        break
                    }
                }
                if ([string]::IsNullOrWhiteSpace($expected)) {
                    Fail "$AssetName has no entry in the release checksum manifest. Refusing to install an unverified binary."
                }
                $actual = (Get-FileHash -LiteralPath $tmpExe -Algorithm SHA256).Hash.ToLowerInvariant()
                if ($expected -ne $actual) {
                    Fail "Checksum mismatch for $AssetName. Expected $expected, got $actual. Refusing to install; the download may be corrupt or tampered with."
                }
                Write-Ok "  checksum verified ($AssetName)"
            }

            Unblock-File -LiteralPath $tmpExe -ErrorAction SilentlyContinue
            $reported = Get-AgentVersion -Exe $tmpExe
            if ($null -eq $reported) {
                Fail "The downloaded nat-backup-agent does not run on this host (architecture mismatch or corrupt download). Aborting without touching $BinPath."
            }
            if ($reported -ne $latest) {
                Write-Warn "The downloaded agent reports version '$reported' but the release tag is '$latest'."
            }

            # Windows refuses to overwrite a running .exe, and an agent left
            # running would keep executing the old code anyway, so whatever is
            # holding the binary has to go down first.
            if ($null -ne $service) { Stop-AgentService -Name $ServiceName -Service $service }

            $running = @(Get-AgentProcess -Exe $BinPath)
            if ($running.Count -gt 0) {
                if (-not $Force) {
                    Fail "nat-backup-agent is running outside of a service (PID $($running.Id -join ', ')) and Windows will not let the installer replace a running .exe. Stop it and re-run, or re-run with -Force to let the installer stop and restart it."
                }
                $agentArgs = Get-AgentArgumentLine -Exe $BinPath
                Stop-BareAgent -Processes $running
                $stoppedProcess = $true
            }

            New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
            try {
                Move-Item -LiteralPath $tmpExe -Destination $BinPath -Force
            } catch {
                Fail "Could not replace ${BinPath}: $($_.Exception.Message). Something is still holding the file; check with: Get-Process $([IO.Path]::GetFileNameWithoutExtension($BinName))"
            }
            $restartNeeded = $true
            Write-Ok "nat-backup-agent installed at $BinPath (version $(Get-AgentVersion -Exe $BinPath))"
        }

        # -- System PATH -----------------------------------------------------
        $machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
        if ($null -eq $machinePath) { $machinePath = "" }
        $entries = $machinePath.Split(';') | Where-Object { $_ -ne "" }
        if ($entries -notcontains $InstallDir) {
            try {
                [Environment]::SetEnvironmentVariable("Path", ((@($entries) + $InstallDir) -join ';'), "Machine")
                Write-Ok "$InstallDir added to the system PATH."
            } catch {
                Write-Warn "Could not update the system PATH. Add it manually: $InstallDir"
            }
        }

        # -- Agent configuration ---------------------------------------------
        if ($configureAgent) {
            New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
            Protect-Path -Path $ConfigDir

            $content  = "server_url: `"$url`"`n"
            $content += "api_key: `"$key`"`n"

            $existing = ""
            if (Test-Path -LiteralPath $ConfigFile) {
                $existing = [System.IO.File]::ReadAllText($ConfigFile)
            }
            if ($existing -eq $content) {
                Write-Ok "Configuration at $ConfigFile is already up to date."
            } else {
                # Create the file, lock it down, and only then write the key, so
                # it is never readable by other local users, not even briefly.
                if (-not (Test-Path -LiteralPath $ConfigFile)) {
                    New-Item -ItemType File -Path $ConfigFile -Force | Out-Null
                }
                Protect-Path -Path $ConfigFile
                [System.IO.File]::WriteAllText($ConfigFile, $content, (New-Object System.Text.UTF8Encoding($false)))
                $restartNeeded = $true
                Write-Ok "Configuration saved to $ConfigFile (SYSTEM and Administrators only)"
            }
        } elseif (Test-Path -LiteralPath $ConfigFile) {
            # Harden a config left behind by an older installer or written by hand.
            Protect-Path -Path $ConfigFile
        }

        # -- Bring the agent back up -----------------------------------------
        # The agent is a console application: it does not implement the Windows
        # Service Control Manager protocol, so this installer never registers it
        # with New-Service/sc.exe (such a service fails to start with error
        # 1053). It does restart a service somebody else set up with a wrapper
        # (NSSM, WinSW, srvany), which is what makes an update actually take
        # effect on the host.
        $agentRunning = $false
        $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

        if ($null -ne $service) {
            $service.Refresh()
            if ($restartNeeded) {
                # A config-only change leaves the service running with the old
                # settings, so stop it here too: Start-Service alone would be a
                # no-op and the update would silently not take effect.
                Stop-AgentService -Name $ServiceName -Service $service
                Write-Info "Starting the $ServiceName service..."
                Start-Service -Name $ServiceName
                Wait-ServiceRunning -Name $ServiceName
                $agentRunning = $true
                Write-Ok "$ServiceName is running version $(Get-AgentVersion -Exe $BinPath)"
            } elseif ($service.Status -eq 'Running') {
                $agentRunning = $true
                Write-Ok "$ServiceName is already running the current version."
            } else {
                Write-Warn "$ServiceName is installed but not running. Start it with: Start-Service $ServiceName"
            }
        } elseif ($stoppedProcess) {
            # Restart it with the very same arguments it was running with; fall
            # back to the standard config path when they could not be read.
            if ([string]::IsNullOrWhiteSpace($agentArgs)) { $agentArgs = "`"$ConfigFile`"" }
            Start-BareAgent -Exe $BinPath -Arguments $agentArgs
            $agentRunning = $true
        } elseif ($restartNeeded) {
            # Nothing was stopped for the binary (it was already current), but
            # the configuration changed under a running agent.
            $running = @(Get-AgentProcess -Exe $BinPath)
            if ($running.Count -eq 0) {
                Write-Warn "The nat-backup-agent is not running: start it so the update takes effect."
            } elseif ($Force) {
                $agentArgs = Get-AgentArgumentLine -Exe $BinPath
                if ([string]::IsNullOrWhiteSpace($agentArgs)) { $agentArgs = "`"$ConfigFile`"" }
                Stop-BareAgent -Processes $running
                Start-BareAgent -Exe $BinPath -Arguments $agentArgs
                $agentRunning = $true
            } else {
                Write-Warn "A nat-backup-agent process is still running with the previous configuration (PID $($running.Id -join ', ')): restart it, or re-run this installer with -Force."
            }
        }

        # -- Result ----------------------------------------------------------
        Write-Host ""
        Write-Host "nat-backup-agent $latest installed successfully." -ForegroundColor Green
        Write-Host ""

        if ($configureAgent) {
            if ($agentRunning) {
                Write-Host "Agent configured and running."
                if ($null -ne $service) {
                    Write-Host "  Status:  Get-Service $ServiceName"
                    Write-Host "  Stop:    Stop-Service $ServiceName"
                }
            } else {
                Write-Host "Agent configured. Start it with:"
                Write-Host "  & `"$BinPath`" `"$ConfigFile`""
            }
        } else {
            Write-Host "Next steps:"
            Write-Host "  1. Create the config file $ConfigFile with:"
            Write-Host "       server_url: http://YOUR_SERVER:8080"
            Write-Host "       api_key:    YOUR_API_KEY"
            Write-Host "     (or re-run this installer with -ServerUrl and -ApiKey, which also"
            Write-Host "      restricts the file to SYSTEM and Administrators)"
            Write-Host "  2. Run:  & `"$BinPath`" `"$ConfigFile`""
        }
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

try {
    Invoke-Install
} catch {
    Write-Host "ERROR $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
