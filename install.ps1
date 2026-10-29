<#
.SYNOPSIS
    Install dtctl (Dynatrace CLI) on Windows.
.DESCRIPTION
    Downloads the latest dtctl release from GitHub and installs it to
    %LOCALAPPDATA%\dtctl. Adds the directory to the user PATH if needed.
.EXAMPLE
    irm https://raw.githubusercontent.com/Dynatrace/dtctl/main/install.ps1 | iex
#>

$ErrorActionPreference = 'Stop'

# Determine architecture
$arch = 'amd64'
try {
    if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') {
        $arch = 'arm64'
    }
} catch {
    # Fallback for older .NET frameworks where RuntimeInformation is unavailable
}

$repoOwner = 'dynatrace-oss'
$repoName = 'dtctl'

# Get latest release
Write-Host "Fetching latest release..." -ForegroundColor Cyan
$release = Invoke-RestMethod "https://api.github.com/repos/$repoOwner/$repoName/releases/latest"
$tag = $release.tag_name
Write-Host "Latest release: $tag" -ForegroundColor Green

# Find download URL
$assetName = "dtctl_$($tag.TrimStart('v'))_windows_$arch.zip"
$asset = $release.assets | Where-Object { $_.name -eq $assetName }
if (-not $asset) {
    # Fallback: match by pattern
    $asset = $release.assets | Where-Object { $_.name -match "windows_$arch\.zip$" }
}
if (-not $asset) {
    Write-Error "Could not find Windows $arch asset in release $tag. Available assets: $($release.assets.name -join ', ')"
    exit 1
}

$downloadUrl = $asset.browser_download_url

# Resolve an existing path to its canonical long form to avoid 8.3 short-name issues
# (e.g. C:\Users\LONGUS~1\...). Returns the path unchanged if it doesn't exist.
function Resolve-LongPath {
    param([string]$Path)
    try {
        if (Test-Path $Path) {
            return (Get-Item $Path).FullName
        }
        # For paths that don't exist yet, resolve the parent directory
        $parent = Split-Path $Path -Parent
        $leaf = Split-Path $Path -Leaf
        if ($parent -and (Test-Path $parent)) {
            return Join-Path (Get-Item $parent).FullName $leaf
        }
    } catch {
        Write-Debug "Resolve-LongPath: could not resolve '$Path': $_"
    }
    return $Path
}

# Install directory
$installDir = Join-Path $env:LOCALAPPDATA 'dtctl'
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}
$installDir = Resolve-LongPath $installDir

# Download and extract
$tempDir = Resolve-LongPath $env:TEMP
$tempZip = Join-Path $tempDir "dtctl-$tag.zip"
Write-Host "Downloading $($asset.name)..." -ForegroundColor Cyan
Invoke-WebRequest -Uri $downloadUrl -OutFile $tempZip -UseBasicParsing
Write-Host "Extracting to $installDir..." -ForegroundColor Cyan
Expand-Archive -Path $tempZip -DestinationPath $installDir -Force

# Clean up temp file (non-fatal — don't fail the install over temp cleanup)
try { Remove-Item $tempZip -Force -ErrorAction SilentlyContinue } catch {}

# Verify the binary exists (check for nested directory from zip structure)
$exePath = Join-Path $installDir 'dtctl.exe'
if (-not (Test-Path $exePath)) {
    # Some zip tools create a nested folder — look one level deeper
    $nested = Get-ChildItem -Path $installDir -Filter 'dtctl.exe' -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($nested) {
        # Move all files from nested directory to install root
        $nestedDir = $nested.DirectoryName
        Get-ChildItem -Path $nestedDir | Move-Item -Destination $installDir -Force -ErrorAction SilentlyContinue
        Remove-Item $nestedDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
if (-not (Test-Path $exePath)) {
    Write-Error "dtctl.exe not found in $installDir after extraction."
    exit 1
}

# Add to PATH if not already present
# Resolve existing PATH entries to long paths before comparing, so an 8.3 short-path
# entry already in PATH doesn't cause the long-path version to be added as a duplicate.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$resolvedEntries = $userPath -split ';' | ForEach-Object { Resolve-LongPath $_ }
if ($installDir -notin $resolvedEntries) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    $env:Path = "$env:Path;$installDir"
    Write-Host "Added $installDir to user PATH." -ForegroundColor Green
} else {
    Write-Host "$installDir is already in PATH." -ForegroundColor Green
}

# Verify
Write-Host ""
$version = & $exePath version 2>&1
Write-Host "Installed: $version" -ForegroundColor Green
Write-Host ""
Write-Host "Installation complete! Run 'dtctl --help' to get started." -ForegroundColor Green
Write-Host ""
Write-Host "Quick setup:" -ForegroundColor Cyan
Write-Host '  dtctl auth login --context my-env --environment "https://YOUR_ENV.apps.dynatrace.com"' -ForegroundColor White
