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
$newRepoKeyword = "<< New Repo >>"

$customEntries = $configFile.customEntries
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

function Clean-ForBash {
    param(
        [string]$incomingVal
    )

    $cur = $incomingVal
    $escapeCharacters = @( " ", "(", ")", "'", "&", ";", "!", "{", "}", "[", "]", "``", "`$", ">", "<", "|", "?", "*", "#", "@" )

    foreach($char in $escapeCharacters) {
        $cur = $cur.Replace($char, "\$char")
    }

    return $cur
}

function Get-TmuxBaseIndex($isWsl = $false) {
    if($isWsl) {
        $baseIndexRaw = wsl tmux show-options -gv base-index 2>$null
    } else {
        $baseIndexRaw = tmux show-options -gv base-index 2>$null
    }

    if([string]::IsNullOrWhiteSpace($baseIndexRaw)) {
        return 0
    }

    [int]$parsedBaseIndex = 0
    if([int]::TryParse($baseIndexRaw.Trim(), [ref]$parsedBaseIndex)) {
        return $parsedBaseIndex
    }

    return 0
}

function Get-NextTmuxWindowIndex($sessionName, $isWsl = $false) {
    [int]$baseIndex = Get-TmuxBaseIndex $isWsl

    if($isWsl) {
        $windowIndexesRaw = wsl tmux list-windows -t $sessionName -F '#{window_index}' 2>$null
    } else {
        $windowIndexesRaw = tmux list-windows -t $sessionName -F '#{window_index}' 2>$null
    }

    [int[]]$windowIndexes = @()
    foreach($indexRaw in $windowIndexesRaw) {
        [int]$parsedWindowIndex = 0
        if([int]::TryParse($indexRaw.Trim(), [ref]$parsedWindowIndex)) {
            $windowIndexes += $parsedWindowIndex
        }
    }

    [int]$candidateWindowIndex = $baseIndex
    while($windowIndexes -contains $candidateWindowIndex) {
        $candidateWindowIndex++
    }

    return $candidateWindowIndex
}

function Get-TmuxDefaultShell($isWsl = $false) {
    if($isWsl) {
        $defaultShellRaw = wsl tmux show-options -gv default-shell 2>$null
    } else {
        $defaultShellRaw = tmux show-options -gv default-shell 2>$null
    }

    if([string]::IsNullOrWhiteSpace($defaultShellRaw)) {
        return "sh"
    }

    return $defaultShellRaw.Trim()
}

function Get-ShellCdCommand($shellName, $pathToCd) {
    $shellCommandRaw = "$shellName".Trim()
    $shellExecutable = ($shellCommandRaw -split '\\s+')[0]
    $shellNameLower = [System.IO.Path]::GetFileName($shellExecutable).ToLowerInvariant()
    $powerShellNames = @("pwsh", "pwsh.exe", "powershell", "powershell.exe")

    if($powerShellNames -contains $shellNameLower) {
        return "cd `"$pathToCd`""
    }

    $escapedPath = Clean-ForBash $pathToCd
    return "cd $escapedPath"
}

function Start-TmuxShellPane($repoOpenPath, $windowIndex, $isWsl = $false) {
   $pathToCd = $repoOpenPath

   if($isWsl -and $preferedShell -ne "pwsh.exe") {
       $convertedPath = wsl wslpath -a "$repoOpenPath" 2>$null
       if(-not [string]::IsNullOrWhiteSpace($convertedPath)) {
           $pathToCd = $convertedPath.Trim()
       }
   }

   $cdCommand = Get-ShellCdCommand $preferedShell $pathToCd

   if($preferedShell -and -not $isWsl) {
       $existingPaneId = tmux display-message -p -t code:$windowIndex '#{pane_id}'
       $newPaneId = tmux split-window -P -F '#{pane_id}' -t code:$windowIndex -v
       tmux send-keys -t $newPaneId "$preferedShell" C-m
       tmux send-keys -t $newPaneId "$cdCommand" C-m
       tmux resize-pane -t $newPaneId -y 20
       tmux send-keys -t $newPaneId "clear" C-m
       tmux select-pane -t $existingPaneId
    } elseif ($preferedShell -and $isWsl) {
       $existingPaneId = wsl tmux display-message -p -t code:$windowIndex '#{pane_id}'
       $newPaneId = wsl tmux split-window -P -F '#{pane_id}' -t code:$windowIndex -v
       wsl tmux send-keys -t $newPaneId "$preferedShell" C-m
       if($preferedShell -eq "pwsh.exe") {
          Start-Sleep -Milliseconds 500
       }
       wsl tmux send-keys -t $newPaneId "$cdCommand" C-m
       wsl tmux send-keys -t $newPaneId "clear" C-m
       wsl tmux resize-pane -t $newPaneId -y 20
       wsl tmux select-pane -t $existingPaneId
    }
}

$rerunWithThisRepoToOpen = $null
do {
    $options = (Get-ChildItem $repoDir).Name
    $options += $cloneKeyword
    $options += $newRepoKeyword
    $options += [string[]]($customEntries.name)

    if($Continuous) {
      $options += $exitKeyword
    }

    [string]$repoToOpen = if([string]::IsNullOrWhitespace($rerunWithThisRepoToOpen)) {
        $options | fzf
    } else {
        $tmp = $rerunWithThisRepoToOpen
        $rerunWithThisRepoToOpen = $null
        $tmp
    }

    $trimmedRepoToOpen = $repoToOpen.Trim()
    if(-not $repoToOpen) {
      continue
    } elseif($trimmedRepoToOpen -eq $exitKeyword) {
      break
    } elseif($trimmedRepoToOpen -eq $cloneKeyword) {
      $repoToClone = Read-Host "Enter the repo to clone:"
      git -C $repoDir clone $repoToClone
      # TODO: figure out how to pull the name of the repo out of here to use to send next time
      # around the loop
      continue
    } elseif($trimmedRepoToOpen -eq $newRepoKeyword) {
        $repoToCreate = Read-Host "Enter the name of the repo to create:"
        if([string]::IsNullOrWhitespace($repoToCreate)) {
            Write-Host "Error, you did not enter the name of a repo to create. Please try again."
            continue
        }
        [string]$pathToCreate = "$repoDir/$repoToCreate"
        mkdir $pathToCreate
        git -C $pathToCreate init
        $rerunWithThisRepoToOpen = $repoToCreate
        continue
    }


    $customOptionsSelected = $customEntries | Where-Object { $_.name -eq $repoToOpen }
    if($customOptionsSelected) {
      if($IsWindows) {
          $repoOpenPath = Invoke-Expression "`"$($customOptionsSelected.paths.win)`""
      } elseif($IsLinux) {
          $repoOpenPath = Invoke-Expression "`"$($customOptionsSelected.paths.linux)`""
      } else {
          Write-Host "Unsupported OS"
          break
      }
    } else {
      $repoOpenPath = "$repoDir/$repoToOpen"
    }

    $solutionFile = Get-ChildItem $repoOpenPath/*.sln

    $openCodeCommands = Get-Command opencode -ErrorAction SilentlyContinue

    $vsOption = if($IsWindows -and $solutionFile) { "vs" } else { $null }
    $nvimWinTmuxOption = if($IsWindows) { "nvim-win-tmux" } else { $null }
    $nvimWinWslTmuxOption = if($IsWindows) { "nvim-wsl-tmux" } else { $null }
    $nvimWinOption = if($IsWindows) { "nvim-win" } else { $null }
    $nvimWslOption = if($IsWindows) { "nvim-wsl" } else { $null }
    $nvimOption = if($IsLinux) { "nvim" } else { $null }
    $nvimTmuxOption = if($IsLinux) { "nvim-tmux" } else { $null }
    $opencodeOption = if($openCodeCommands.Count -ne 0) { "opencode-here" } else { $null }
    $codeOption = if(-not $configFile.isServer) { "code" } else { $null }
    $runOptions = @(
        $nvimWinTmuxOption,
        $nvimWinWslTmuxOption,
        $nvimWinOption,
        $nvimWslOption,
        $nvimTmuxOption,
        $nvimOption,
        $vsOption,
        $codeOption,
        $opencodeOption,
        "cd-here"
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
    } elseif($selectedOption -eq "opencode-here") {
      Set-Location $repoOpenPath
      opencode
    } elseif($selectedOption -eq "cd-here") {
      Set-Location $repoOpenPath
      & $preferedShell
    } elseif($selectedOption -eq "nvim-wsl-tmux") {
       wsl tmux new-session -d -s code -n op -c $wslRepoDir
       [int]$targetWindowIndex = Get-NextTmuxWindowIndex "code" $true
       $createdWindowIndexRaw = wsl tmux new-window -P -F '#{window_index}' -d -t code:$targetWindowIndex -n "$repoToOpen"
       [int]$newWindowIndex = 0
       if([string]::IsNullOrWhiteSpace($createdWindowIndexRaw) -or -not [int]::TryParse($createdWindowIndexRaw.Trim(), [ref]$newWindowIndex)) {
           Write-Host "Failed to create tmux window for $repoToOpen"
           continue
       }
       [string]$wslRepoOpenPath = "$wslRepoDir/$repoToOpen"
       [string]$mainPaneShell = Get-TmuxDefaultShell $true
       [string]$mainPaneCdCommand = Get-ShellCdCommand $mainPaneShell $wslRepoOpenPath
       wsl tmux send-keys -t code:$newWindowIndex "$mainPaneCdCommand" C-m
       wsl tmux send-keys -t code:$newWindowIndex "nvim ." C-m

       Start-TmuxShellPane $repoOpenPath $newWindowIndex $true

       Write-Host "Opening nvim in tmux session 'code'"
    } elseif($selectedOption -eq "nvim-win-tmux") {
       Update-Repo $repoOpenPath
       wsl tmux new-session -d -s code -n op -c $wslRepoDir
       [int]$targetWindowIndex = Get-NextTmuxWindowIndex "code" $true
       $createdWindowIndexRaw = wsl tmux new-window -P -F '#{window_index}' -d -t code:$targetWindowIndex -n "$repoToOpen"
       [int]$newWindowIndex = 0
       if([string]::IsNullOrWhiteSpace($createdWindowIndexRaw) -or -not [int]::TryParse($createdWindowIndexRaw.Trim(), [ref]$newWindowIndex)) {
           Write-Host "Failed to create tmux window for $repoToOpen"
           continue
       }
       [string]$mainPaneShell = "pwsh.exe"
       [string]$mainPaneCdCommand = Get-ShellCdCommand $mainPaneShell $repoOpenPath
       wsl tmux send-keys -t code:$newWindowIndex "pwsh.exe" C-m
       Start-Sleep 2
       wsl tmux send-keys -t code:$newWindowIndex "$mainPaneCdCommand" C-m
       wsl tmux send-keys -t code:$newWindowIndex "nvim ." C-m

       Start-TmuxShellPane $repoOpenPath $newWindowIndex $true

       Write-Host "Opening nvim in tmux session 'code'"
    } elseif($selectedOption -eq "nvim-tmux") {
       Update-Repo $repoOpenPath
       tmux new-session -d -s code -n op -c $wslRepoDir
       [int]$targetWindowIndex = Get-NextTmuxWindowIndex "code"
       $createdWindowIndexRaw = tmux new-window -P -F '#{window_index}' -d -t code:$targetWindowIndex -n "$repoToOpen"
       [int]$newWindowIndex = 0
       if([string]::IsNullOrWhiteSpace($createdWindowIndexRaw) -or -not [int]::TryParse($createdWindowIndexRaw.Trim(), [ref]$newWindowIndex)) {
           Write-Host "Failed to create tmux window for $repoToOpen"
           continue
       }
       [string]$mainPaneShell = Get-TmuxDefaultShell
       [string]$mainPaneCdCommand = Get-ShellCdCommand $mainPaneShell $repoOpenPath
       tmux send-keys -t code:$newWindowIndex "$mainPaneCdCommand" C-m
       tmux send-keys -t code:$newWindowIndex "nvim ." C-m
         
       Start-TmuxShellPane $repoOpenPath $newWindowIndex

       Write-Host "Opening nvim in tmux session 'code'"
    } else {
      Write-Output "No option selected"
    }
} while($Continuous)
