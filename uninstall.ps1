<#
.SYNOPSIS
    Uninstall dtctl (Dynatrace CLI) on Windows.
.DESCRIPTION
    Removes dtctl from %LOCALAPPDATA%\dtctl and removes that directory from the
    user PATH. Optional switches remove XDG-based config/cache/data directories.
.EXAMPLE
    irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/uninstall.ps1 | iex
.EXAMPLE
    .\uninstall.ps1 -RemoveConfig -RemoveCache -RemoveData
#>

[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [switch]$RemoveConfig,
    [switch]$RemoveCache,
    [switch]$RemoveData
)

$ErrorActionPreference = 'Stop'

function Resolve-LongPath {
    param([string]$Path)

    if ([string]::IsNullOrWhiteSpace($Path)) {
        return $Path
    }

    try {
        if (Test-Path -LiteralPath $Path) {
            return (Get-Item -LiteralPath $Path).FullName
        }

        $parent = Split-Path -Path $Path -Parent
        $leaf = Split-Path -Path $Path -Leaf
        if ($parent -and (Test-Path -LiteralPath $parent)) {
            return Join-Path (Get-Item -LiteralPath $parent).FullName $leaf
        }
    } catch {
        Write-Debug "Resolve-LongPath: could not resolve '$Path': $_"
    }

    return $Path
}

function Get-NormalizedPath {
    param([string]$Path)

    if ([string]::IsNullOrWhiteSpace($Path)) {
        return ''
    }

    try {
        return [System.IO.Path]::GetFullPath((Resolve-LongPath $Path)).TrimEnd('\\')
    } catch {
        return (Resolve-LongPath $Path).TrimEnd('\\')
    }
}

function Remove-DirectorySafe {
    param(
        [Parameter(Mandatory = $true)][string]$TargetPath,
        [Parameter(Mandatory = $true)][string]$AllowedRoot,
        [Parameter(Mandatory = $true)][string]$Label
    )

    $normalizedTarget = Get-NormalizedPath $TargetPath
    $normalizedRoot = Get-NormalizedPath $AllowedRoot

    if ([string]::IsNullOrWhiteSpace($normalizedTarget) -or [string]::IsNullOrWhiteSpace($normalizedRoot)) {
        Write-Warning "Skipping $Label cleanup due to invalid paths."
        return
    }

    $insideRoot = $normalizedTarget.StartsWith($normalizedRoot + '\', [System.StringComparison]::OrdinalIgnoreCase) -or
        $normalizedTarget.Equals($normalizedRoot, [System.StringComparison]::OrdinalIgnoreCase)

    if (-not $insideRoot) {
        Write-Warning "Skipping $Label cleanup: '$normalizedTarget' is outside allowed root '$normalizedRoot'."
        return
    }

    if (Test-Path -LiteralPath $TargetPath) {
        if ($PSCmdlet.ShouldProcess($TargetPath, "Remove $Label directory")) {
            Remove-Item -LiteralPath $TargetPath -Recurse -Force
            Write-Host "Removed ${Label}: $TargetPath" -ForegroundColor Green
        }
    } else {
        Write-Host "$Label not found: $TargetPath" -ForegroundColor Yellow
    }
}

$installDir = Resolve-LongPath (Join-Path $env:LOCALAPPDATA 'dtctl')

Write-Host "Uninstalling dtctl from Windows user profile..." -ForegroundColor Cyan

# Remove installed binary directory.
Remove-DirectorySafe -TargetPath $installDir -AllowedRoot $env:LOCALAPPDATA -Label 'install directory'

# Remove install directory from User PATH, if present.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($null -eq $userPath) { $userPath = '' }

$normalizedInstallDir = Get-NormalizedPath $installDir
$entries = $userPath -split ';' | Where-Object { $_ -and $_.Trim() -ne '' }
$newEntries = @()
$removedEntries = @()

foreach ($entry in $entries) {
    $normalizedEntry = Get-NormalizedPath $entry
    if ($normalizedInstallDir -and $normalizedEntry -and $normalizedEntry.Equals($normalizedInstallDir, [System.StringComparison]::OrdinalIgnoreCase)) {
        $removedEntries += $entry
    } else {
        $newEntries += $entry
    }
}

if ($removedEntries.Count -gt 0) {
    $newUserPath = $newEntries -join ';'
    if ($PSCmdlet.ShouldProcess('User PATH', "Remove dtctl path entry '$installDir'")) {
        [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')

        # Best effort: reflect PATH changes for the current process as well.
        $machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
        if ($null -eq $machinePath) { $machinePath = '' }
        if ($newUserPath) {
            $env:Path = "$machinePath;$newUserPath"
        } else {
            $env:Path = $machinePath
        }

        Write-Host "Removed PATH entries:" -ForegroundColor Green
        $removedEntries | ForEach-Object { Write-Host "  - $_" -ForegroundColor Green }
    }
} else {
    Write-Host "No dtctl install path entry found in User PATH." -ForegroundColor Yellow
}

# Optional cleanup for XDG-based directories.
$configRoot = if ($env:XDG_CONFIG_HOME) { $env:XDG_CONFIG_HOME } else { $env:APPDATA }
$cacheRoot = if ($env:XDG_CACHE_HOME) { $env:XDG_CACHE_HOME } else { $env:LOCALAPPDATA }
$dataRoot = if ($env:XDG_DATA_HOME) { $env:XDG_DATA_HOME } else { $env:LOCALAPPDATA }

$configDir = Join-Path $configRoot 'dtctl'
$cacheDir = Join-Path $cacheRoot 'dtctl'
$dataDir = Join-Path $dataRoot 'dtctl'

if ($RemoveConfig) {
    Remove-DirectorySafe -TargetPath $configDir -AllowedRoot $configRoot -Label 'config directory'
}
if ($RemoveCache) {
    Remove-DirectorySafe -TargetPath $cacheDir -AllowedRoot $cacheRoot -Label 'cache directory'
}
if ($RemoveData) {
    Remove-DirectorySafe -TargetPath $dataDir -AllowedRoot $dataRoot -Label 'data directory'
}

Write-Host ''
Write-Host 'Uninstall complete.' -ForegroundColor Green
if (-not ($RemoveConfig -or $RemoveCache -or $RemoveData)) {
    Write-Host 'Note: configuration, cache, and data were kept. Use -RemoveConfig -RemoveCache -RemoveData for full cleanup.' -ForegroundColor Yellow
}
Write-Host 'If dtctl still appears in a shell, restart your terminal or IDE session.' -ForegroundColor Cyan
