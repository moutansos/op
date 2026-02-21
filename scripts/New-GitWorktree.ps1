$branchName = Read-Host "Enter the name of the new branch:"
$currentDirectoryName = Split-Path -Leaf (Get-Location)

$worktreeDirectoryName = "$currentDirectoryName-$branchName"
$worktreeDirectoryPath = Join-Path ".." $worktreeDirectoryName

git branch $branchName
git worktree add $worktreeDirectoryPath $branchName
