$configFileRaw = Get-Content $PSScriptRoot/config.json -Raw
$configFile = $configFileRaw | ConvertFrom-Json

$repoDir = $configFile.repoDirectory
$wslRepoDir = $configFile.wslRepoDirectory

$repoToOpen = (Get-ChildItem $repoDir).Name | fzf
$repoOpenPath = "$repoDir/$repoToOpen"

$solutionFile = Get-ChildItem $repoOpenPath/*.sln

$vsOption = if($IsWindows -and $solutionFile) { "vs" } else { $null }
$nvimWinTmuxOption = if($IsWindows) { "nvim-win-tmux" } else { $null }
$nvimWinWslTmuxOption = if($IsWindows) { "nvim-wsl-tmux" } else { $null }
$nvimWinOption = if($IsWindows) { "nvim-win" } else { $null }
$nvimWslOption = if($IsWindows) { "nvim-wsl" } else { $null }
$nvimOption = if($IsLinux) { "nvim" } else { $null }
$nvimTmuxOption = if($IsLinux) { "nvim-tmux" } else { $null }
$runOptions = @(
    $nvimWinTmuxOption,
    $nvimWinWslTmuxOption,
    $nvimWinOption,
    $nvimWslOption,
    $nvimTmuxOption,
    $nvimOption,
    $vsOption,
    "code"
)
$selectedOptions = $runOptions | Where-Object { $null -ne $_ }
$selectedOption = $selectedOptions | fzf

if($selectedOption -eq "vs") {
  Invoke-Item $solutionFile 
} elseif($selectedOption -in @("nvim-win", "nvim")) {
  Set-Location $repoOpenPath
  nvim $repoOpenPath
} elseif($selectedOption -eq "nvim-wsl") {
  wsl nvim $wslRepoDir/$repoToOpen
} elseif($selectedOption -eq "code") {
  code $repoOpenPath
} elseif($selectedOption -eq "nvim-wsl-tmux") {
   wsl tmux new-session -d -s code -c $wslRepoDir
   [int]$paneCount = wsl bash -c "tmux list-windows -t code | wc -l"
   [int]$newPane = $paneCount
   wsl tmux new-window -t code:$newPane -n $repoToOpen
   wsl tmux send-keys -t code:$newPane "nvim $wslRepoDir/$repoToOpen" C-m
   Write-Host "Opening nvim in tmux session 'code'"
} elseif($selectedOption -eq "nvim-win-tmux") {
   wsl tmux new-session -d -s code -c $wslRepoDir
   [int]$paneCount = wsl bash -c "tmux list-windows -t code | wc -l"
   [int]$newPane = $paneCount
   wsl tmux new-window -t code:$newPane -n $repoToOpen
   wsl tmux send-keys -t code:$newPane "pwsh.exe" C-m
   Start-Sleep 2
   wsl tmux send-keys -t code:$newPane "cd $repoOpenPath" C-m
   wsl tmux send-keys -t code:$newPane "nvim $repoOpenPath" C-m
   Write-Host "Opening nvim in tmux session 'code'"
} elseif($selectedOption -eq "nvim-tmux") {
   tmux new-session -d -s code -c $wslRepoDir
   [int]$paneCount = tmux list-windows -t code | wc -l
   [int]$newPane = $paneCount
   tmux new-window -t code:$newPane -n $repoToOpen
   tmux send-keys -t code:$newPane "cd $repoOpenPath" C-m
   tmux send-keys -t code:$newPane "nvim $repoOpenPath" C-m
   Write-Host "Opening nvim in tmux session 'code'"
} else {
  Write-Output "No option selected"
}


