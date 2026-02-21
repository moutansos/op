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
$customCommands = $configFile.customCommands
$preferedShell = $configFile.preferedShell

$allCommands = $customCommands + @(
    @{
        name = "new-worktree"
        command = "$PSScriptRoot/scripts/New-GitWorktree.ps1"
        runInPreferredShell = $false
    }
)

function Invoke-HereInPreferredShell {
    param(
        [string]$command,
        [string]$location
    )

    $command = $command.Replace("{{oproot}}", "$PSScriptRoot")

    if($preferedShell -eq "pwsh.exe") {
        pwsh.exe -WorkingDirectory $location -NoExit -c "$command"
    } elseif($preferedShell -eq "powershell.exe") {
        powershell.exe -WorkingDirectory $location -NoExit -c "$command" 
    } elseif($preferedShell -eq "pwsh") {
        pwsh -WorkingDirectory $location -NoExit -c "$command"
    } elseif($preferedShell -eq "bash") {
        Set-Location $location
        bash -i -c "$command || true; exec bash -i" -cd "$location"
    } elseif($preferedShell -eq "zsh") {
        Set-Location $location
        zsh -i -c "$command || true; exec zsh -i" -cd "$location"
    } else {
        Set-Location $location
        Invoke-Expression $command
    }
}

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
        $baseIndexRaw = wsl --exec tmux show-options -gv base-index 2>$null
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
        $windowIndexesRaw = wsl --exec tmux list-windows -t $sessionName -F '#{window_index}' 2>$null
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
        $defaultShellRaw = wsl --exec tmux show-options -gv default-shell 2>$null
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

function Ensure-TmuxSession($sessionName, $startDirectory, $isWsl = $false) {
    if($isWsl) {
        wsl --exec tmux has-session -t $sessionName 2>$null
        if($LASTEXITCODE -ne 0) {
            wsl --exec tmux new-session -d -s $sessionName -n op -c $startDirectory
        }
    } else {
        tmux has-session -t $sessionName 2>$null
        if($LASTEXITCODE -ne 0) {
            tmux new-session -d -s $sessionName -n op -c $startDirectory
        }
    }
}

function Start-TmuxShellPane($repoOpenPath, $windowIndex, $isWsl = $false) {
   $pathToCd = $repoOpenPath

   if($isWsl -and $preferedShell -ne "pwsh.exe") {
       $convertedPath = wsl --exec wslpath -a "$repoOpenPath" 2>$null
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
       $existingPaneId = wsl --exec tmux display-message -p -t code:$windowIndex '#{pane_id}'
       $newPaneId = wsl --exec tmux split-window -P -F '#{pane_id}' -t code:$windowIndex -v
       wsl --exec tmux send-keys -t $newPaneId "$preferedShell" C-m
       if($preferedShell -eq "pwsh.exe") {
          Start-Sleep -Milliseconds 500
       }
       wsl --exec tmux send-keys -t $newPaneId "$cdCommand" C-m
       wsl --exec tmux send-keys -t $newPaneId "clear" C-m
       wsl --exec tmux resize-pane -t $newPaneId -y 20
       wsl --exec tmux select-pane -t $existingPaneId
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
        $codeOption,
        "cd-here"
    )
    $runOptions += $allCommands | ForEach-Object { $_.name }
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
    } elseif($selectedOption -eq "cd-here") {
      Set-Location $repoOpenPath
      & $preferedShell
    } elseif($selectedOption -eq "nvim-wsl-tmux") {
       Ensure-TmuxSession "code" $wslRepoDir $true
       [int]$targetWindowIndex = Get-NextTmuxWindowIndex "code" $true
       $createdWindowIndexRaw = wsl --exec tmux new-window -P -F '#{window_index}' -d -t code:$targetWindowIndex -n "$repoToOpen"
       [int]$newWindowIndex = 0
       if([string]::IsNullOrWhiteSpace($createdWindowIndexRaw) -or -not [int]::TryParse($createdWindowIndexRaw.Trim(), [ref]$newWindowIndex)) {
           Write-Host "Failed to create tmux window for $repoToOpen"
           continue
       }
       [string]$wslRepoOpenPath = "$wslRepoDir/$repoToOpen"
       [string]$mainPaneShell = Get-TmuxDefaultShell $true
       [string]$mainPaneCdCommand = Get-ShellCdCommand $mainPaneShell $wslRepoOpenPath
       wsl --exec tmux send-keys -t code:$newWindowIndex "$mainPaneCdCommand" C-m
       wsl --exec tmux send-keys -t code:$newWindowIndex "nvim ." C-m

       Start-TmuxShellPane $repoOpenPath $newWindowIndex $true

       Write-Host "Opening nvim in tmux session 'code'"
    } elseif($selectedOption -eq "nvim-win-tmux") {
       Update-Repo $repoOpenPath
       Ensure-TmuxSession "code" $wslRepoDir $true
       [int]$targetWindowIndex = Get-NextTmuxWindowIndex "code" $true
       $createdWindowIndexRaw = wsl --exec tmux new-window -P -F '#{window_index}' -d -t code:$targetWindowIndex -n "$repoToOpen"
       [int]$newWindowIndex = 0
       if([string]::IsNullOrWhiteSpace($createdWindowIndexRaw) -or -not [int]::TryParse($createdWindowIndexRaw.Trim(), [ref]$newWindowIndex)) {
           Write-Host "Failed to create tmux window for $repoToOpen"
           continue
       }
       [string]$mainPaneShell = "pwsh.exe"
       [string]$mainPaneCdCommand = Get-ShellCdCommand $mainPaneShell $repoOpenPath
       wsl --exec tmux send-keys -t code:$newWindowIndex "pwsh.exe" C-m
       Start-Sleep 2
       wsl --exec tmux send-keys -t code:$newWindowIndex "$mainPaneCdCommand" C-m
       wsl --exec tmux send-keys -t code:$newWindowIndex "nvim ." C-m

       Start-TmuxShellPane $repoOpenPath $newWindowIndex $true

       Write-Host "Opening nvim in tmux session 'code'"
    } elseif($selectedOption -eq "nvim-tmux") {
       Update-Repo $repoOpenPath
       Ensure-TmuxSession "code" $wslRepoDir
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
      $commandFound = $false
      foreach ($customCommand in $allCommands) {
        if($customCommand.name -eq $selectedOption) {
          $commandFound = $true
          $commandToRun = $customCommand.command
          $commandToRun = $commandToRun.Replace("{{path}}", "`"$repoOpenPath`"")

          if($customCommand.runInPreferredShell -eq $false) {
              Set-Location $repoOpenPath
              Invoke-Expression $commandToRun
              break
          }

          Invoke-HereInPreferredShell $commandToRun $repoOpenPath
          break
        }
      }
      if(-not $commandFound) {
        Write-Output "No option selected"
      }
    }
} while($Continuous)
