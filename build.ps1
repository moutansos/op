[CmdletBinding()]
param(
    [switch]$Build,
    [switch]$Quiet,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'

$BuildOutputDir = Join-Path $PSScriptRoot '.build_output'
$LinuxBinary = Join-Path $BuildOutputDir 'op'
$WindowsBinary = Join-Path $BuildOutputDir 'op.exe'

function Write-Usage {
    @"
Usage: ./build.ps1 [options]

Options:
  -Build    Build the Linux/WSL op binary and the Windows proxy, then install
            the Linux binary into WSL
  -Quiet    Suppress stage banners
  -Help     Show this help message

The WSL distribution and install path come from OP_WSL_DISTRO and OP_WSL_OP,
then %APPDATA%\op\wsl.json, then the default distribution and ~/.local/bin/op.

Build metadata can be overridden with VERSION, COMMIT, and BUILD_DATE.
"@
}

function Write-Stage([string]$StageName) {
    if ($Quiet) {
        return
    }
    [Console]::Error.WriteLine("`n==========================================")
    [Console]::Error.WriteLine("Starting stage: $StageName")
    [Console]::Error.WriteLine("==========================================`n")
}

function Get-GoArch {
    switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'X64' { 'amd64' }
        'Arm64' { 'arm64' }
        default { throw "Unsupported architecture: $_" }
    }
}

function Get-BuildLdflags {
    $version = if ($env:VERSION) { $env:VERSION } else { 'dev' }
    $commit = $env:COMMIT
    if (-not $commit) {
        $commit = git -C $PSScriptRoot rev-parse --short HEAD 2>$null
        if ($LASTEXITCODE -ne 0 -or -not $commit) {
            $commit = 'unknown'
        }
    }
    $date = if ($env:BUILD_DATE) { $env:BUILD_DATE } else { (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ') }
    "-X main.version=$version -X main.commit=$commit -X main.date=$date"
}

function Invoke-GoBuild([string]$GoOS, [string]$GoArch, [string]$Ldflags, [string]$Output) {
    $names = 'GOOS', 'GOARCH', 'CGO_ENABLED'
    $saved = @{}
    foreach ($name in $names) {
        $saved[$name] = [Environment]::GetEnvironmentVariable($name)
    }
    Push-Location $PSScriptRoot
    try {
        $env:GOOS = $GoOS
        $env:GOARCH = $GoArch
        $env:CGO_ENABLED = '0'
        go build -trimpath -ldflags $Ldflags -o $Output ./cmd/op | Out-Host
        $LASTEXITCODE
    }
    finally {
        Pop-Location
        foreach ($name in $names) {
            [Environment]::SetEnvironmentVariable($name, $saved[$name])
        }
    }
}

function Get-WslSettings {
    $settings = @{ Distro = $null; Op = $null }
    $path = Join-Path $env:APPDATA 'op\wsl.json'
    if (Test-Path -LiteralPath $path) {
        $json = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
        $settings.Distro = $json.distro
        $settings.Op = $json.op
    }
    if ($env:OP_WSL_DISTRO) {
        $settings.Distro = $env:OP_WSL_DISTRO
    }
    if ($env:OP_WSL_OP) {
        $settings.Op = $env:OP_WSL_OP
    }
    if ($settings.Op -and -not $settings.Op.StartsWith('/')) {
        throw "The WSL op path must be an absolute Linux path: $($settings.Op)"
    }
    $settings
}

function Install-LinuxBinary {
    $settings = Get-WslSettings
    $wslArgs = @()
    if ($settings.Distro) {
        $wslArgs += '--distribution', $settings.Distro
    }
    $installScript = 'dst=${2:-$HOME/.local/bin/op}; if [ ! "$1" -ef "$dst" ]; then install -Dm755 "$1" "$dst" || exit; fi; printf ''Installed %s\n'' "$dst" >&2'
    $scriptArgs = @('.build_output/op')
    if ($settings.Op) {
        $scriptArgs += $settings.Op
    }
    wsl.exe @wslArgs --cd $PSScriptRoot --exec sh -c $installScript sh @scriptArgs | Out-Host
    $LASTEXITCODE
}

if ($Help -or -not $Build) {
    Write-Usage
    exit 0
}

if ($Build) {
    $goArch = Get-GoArch
    $ldflags = Get-BuildLdflags
    New-Item -ItemType Directory -Force -Path $BuildOutputDir | Out-Null

    Write-Stage 'Building Linux/WSL Application'
    $exitCode = Invoke-GoBuild 'linux' $goArch $ldflags $LinuxBinary
    if ($exitCode -ne 0) {
        exit $exitCode
    }
    [Console]::Error.WriteLine("Built $LinuxBinary")

    Write-Stage 'Building Windows WSL Proxy'
    $exitCode = Invoke-GoBuild 'windows' $goArch $ldflags $WindowsBinary
    if ($exitCode -ne 0) {
        exit $exitCode
    }
    [Console]::Error.WriteLine("Built $WindowsBinary")

    Write-Stage 'Installing Linux/WSL Application'
    $exitCode = Install-LinuxBinary
    if ($exitCode -ne 0) {
        exit $exitCode
    }
}
