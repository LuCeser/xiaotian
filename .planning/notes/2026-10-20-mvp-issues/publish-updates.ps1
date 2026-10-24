$ErrorActionPreference = 'Stop'
$taskDir = Join-Path $PSScriptRoot '.'
$changes = Get-Content (Join-Path $taskDir 'updates.json') -Raw | ConvertFrom-Json
$originals = Get-Content (Join-Path $taskDir 'before.json') -Raw | ConvertFrom-Json
foreach ($change in $changes) {
    $number = [int]$change.number
    $original = $originals | Where-Object number -eq $number
    $currentText = gh issue view $number --repo LuCeser/xiaotian --json title,body,labels
    if ($LASTEXITCODE -ne 0) { throw "Read failed: $number" }
    $current = $currentText | ConvertFrom-Json
    $expectedBody = (Get-Content -LiteralPath $change.bodyFile -Raw).TrimEnd()
    if ($current.title -eq $change.title -and $current.body.TrimEnd() -eq $expectedBody) {
        Write-Output "Already updated #$number"
        continue
    }
    if ($current.title -ne $original.title -or $current.body -ne $original.body -or (($current.labels.name | Sort-Object) -join ',') -ne (($original.labels.name | Sort-Object) -join ',')) {
        throw "Issue #$number changed since snapshot; review before updating"
    }
    $ghArgs = @('issue','edit',"$number",'--repo','LuCeser/xiaotian','--title',$change.title,'--body-file',$change.bodyFile)
    foreach ($label in $change.addLabels) { $ghArgs += @('--add-label',$label) }
    foreach ($label in $change.removeLabels) { $ghArgs += @('--remove-label',$label) }
    & gh @ghArgs
    if ($LASTEXITCODE -ne 0) { throw "Update failed: $number" }
    Add-Content -LiteralPath (Join-Path $taskDir 'published.log') -Value "Updated #$number" -Encoding utf8
}
