param(
    [switch]$Continuous,
    [switch]$NoRepoUpdate
)

$configFileRaw = Get-Content $PSScriptRoot/config.json -Raw
$configFile = $configFileRaw | ConvertFrom-Json

$repoDir = $configFile.repoDirectory
$wslRepoDir = $configFile.wslRepoDirectory
$exitKeyword = "<< Exit >>"
$cloneKeyword = "<< Clone >>"

$preferedShell = $configFile.preferedShell

function Update-Repo {
    param(
        [string]$repoDir
    )

    if($NoRepoUpdate) {
        return
    }

    if(-not (Test-Path $repoDir/.git)) {
        Write-Host "Repo $repoDir is not a git repo"
        return
    }

    $result = git -C $repoDir status
    if(-not ($result -like "*Nothing to commit*")) {
        Write-Host "Repo $repoDir has uncommitted changes"
        return
    }
    
    Write-Host "Updating repo $repoDir"
    git -C $repoDir pull
}

function Start-TmuxShellPane($repoOpenPath, $isWsl = $false) {
   if($preferedShell -and -not $isWsl) {
       tmux split-window -t code:$newPane -v
       tmux send-keys -t code:$newPane.1 "$preferedShell" C-m
       tmux send-keys -t code:$newPane.1 "cd $repoOpenPath" C-m
       tmux select-pane -t code:$newPane.0
   } elseif ($preferedShell -and $isWsl) {
       wsl tmux split-window -t code:$newPane -v
       wsl tmux send-keys -t code:$newPane.1 "$preferedShell" C-m
       if($preferedShell -eq "pwsh.exe") {
           Start-Sleep 1
       }
       wsl tmux send-keys -t code:$newPane.1 "cd $repoOpenPath" C-m
       wsl tmux select-pane -t code:$newPane.0
   }
}

do {
    $options = (Get-ChildItem $repoDir).Name
    $options += $cloneKeyword

    if($Continuous) {
      $options += $exitKeyword
    }

    [string]$repoToOpen = $options | fzf

    if(-not $repoToOpen) {
      continue
    } elseif($repoToOpen.Trim() -eq $exitKeyword) {
      break
    } elseif($repoToOpen.Trim() -eq $cloneKeyword) {
      $repoToClone = Read-Host "Enter the repo to clone:"
      git -C $repoDir clone $repoToClone
      continue
    }

    $repoOpenPath = "$repoDir/$repoToOpen"
    $solutionFile = Get-ChildItem $repoOpenPath/*.sln

    $vsOption = if($IsWindows -and $solutionFile) { "vs" } else { $null }
    $nvimWinTmuxOption = if($IsWindows) { "nvim-win-tmux" } else { $null }
    $nvimWinWslTmuxOption = if($IsWindows) { "nvim-wsl-tmux" } else { $null }
    $nvimWinOption = if($IsWindows) { "nvim-win" } else { $null }
    $nvimWslOption = if($IsWindows) { "nvim-wsl" } else { $null }
    $nvimOption = if($IsLinux) { "nvim" } else { $null }
    $nvimTmuxOption = if($IsLinux) { "nvim-tmux" } else { $null }
    $codeOption = if(-not $configFile.isServer) { "code" } else { $null }
    $runOptions = @(
        $nvimWinTmuxOption,
        $nvimWinWslTmuxOption,
        $nvimWinOption,
        $nvimWslOption,
        $nvimTmuxOption,
        $nvimOption,
        $vsOption,
        $codeOption
    )
    $selectedOptions = $runOptions | Where-Object { $null -ne $_ }
    $selectedOption = $selectedOptions | fzf

    if($selectedOption -eq "vs") {
      Invoke-Item $solutionFile 
    } elseif($selectedOption -in @("nvim-win", "nvim")) {
      Set-Location $repoOpenPath
      Update-Repo $repoOpenPath
      nvim $repoOpenPath
    } elseif($selectedOption -eq "nvim-wsl") {
      wsl nvim $wslRepoDir/$repoToOpen
    } elseif($selectedOption -eq "code") {
      code $repoOpenPath
    } elseif($selectedOption -eq "nvim-wsl-tmux") {
       wsl tmux new-session -d -s code -c $wslRepoDir
       [int]$newPane = wsl bash -c "tmux new-window -P -d -t code -n $repoToOpen | cut -d' ' -f2 | cut -d':' -f2 | cut -d'.' -f1 "
       [string]$wslRepoOpenPath = "$wslRepoDir/$repoToOpen"
       wsl tmux send-keys -t code:$newPane "nvim $wslRepoOpenPath" C-m

       Start-TmuxShellPane $repoOpenPath $true

       Write-Host "Opening nvim in tmux session 'code'"
    } elseif($selectedOption -eq "nvim-win-tmux") {
       Update-Repo $repoOpenPath
       wsl tmux new-session -d -s code -c $wslRepoDir
       [int]$newPane = wsl bash -c "tmux new-window -P -d -t code -n $repoToOpen | cut -d' ' -f2 | cut -d':' -f2 | cut -d'.' -f1 "
       wsl tmux send-keys -t code:$newPane "pwsh.exe" C-m
       Start-Sleep 2
       wsl tmux send-keys -t code:$newPane "cd $repoOpenPath" C-m
       wsl tmux send-keys -t code:$newPane "nvim $repoOpenPath" C-m

       Start-TmuxShellPane $repoOpenPath $true

       Write-Host "Opening nvim in tmux session 'code'"
    } elseif($selectedOption -eq "nvim-tmux") {
       Update-Repo $repoOpenPath
       tmux new-session -d -s code -c $wslRepoDir
       [int]$paneCount = tmux list-windows -t code | wc -l
       [int]$newPane = $paneCount
       tmux new-window -t code:$newPane -n $repoToOpen
       tmux send-keys -t code:$newPane "cd $repoOpenPath" C-m
       tmux send-keys -t code:$newPane "nvim $repoOpenPath" C-m
        
       Start-TmuxShellPane $repoOpenPath

       Write-Host "Opening nvim in tmux session 'code'"
    } else {
      Write-Output "No option selected"
    }
} while($Continuous)

