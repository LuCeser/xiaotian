$ErrorActionPreference = 'Stop'
function Replace-Required([string]$body, [string]$old, [string]$new) {
    if (-not $body.Contains($old)) { throw "Expected text not found: $old" }
    return $body.Replace($old, $new)
}
foreach ($issueNumber in @(55,1,58)) {
    $before = Get-Content (Join-Path $PSScriptRoot "isolation-before-$issueNumber.json") -Raw | ConvertFrom-Json
    $body = $before.body
    if ($issueNumber -eq 55) {
        $body = Replace-Required $body '- 默认判断和用户指定两种情况下，一次默写都只使用最终确定的一个学科；按该学科筛选词条，不混入另一学科的词语。' "- 默认判断和用户指定两种情况下，一次默写都只使用最终确定的一个学科；按该学科筛选词条，不混入另一学科的词语。`n- 各学科的候选词、本轮词列表与任务进度相互隔离，不跨学科补词或串用任务状态。`n- 当前学科可用词量不足时，按实际可用数量开始；没有可用词时明确提示，不启动空默写，也不借用其他学科内容。"
        $body = Replace-Required $body '- [ ] 所选学科词量不足时，不使用另一学科的内容补足；具体反馈按确认后的规则验收。' "- [ ] 当前学科可用词量不足时按实际数量开始。例如请求 10 词、该学科只有 3 个可用词时，仅默写这 3 词，不从另一学科补足。`n- [ ] 当前学科没有可用词时给出明确提示，不启动空任务，不自动改用另一学科。`n- [ ] 连续发起英语、语文任务时，各自候选词、出题列表与本轮进度不串用。"
        $body = Replace-Required $body '### 待复现信息与剩余规则' '### 实施时补充的复现信息'
        $body = Replace-Required $body "- 词量不足时，是按实际数量开始，还是要求用户先补充词库？`n" ''
        $body = Replace-Required $body '- 原问题发生在哪个页面或语音入口，启动时是否已经明确学科？' '- 原问题发生在哪个页面或语音入口，启动时是否已经明确学科？可由 Agent 检查入口并复现采集；以上产品规则已确认，不以补齐历史现场信息作为开始排查的前提。'
    } elseif ($issueNumber -eq 1) {
        $body = Replace-Required $body '词列表按最终学科筛选；无可用词时明确反馈，数量不足不跨学科补齐。' '各学科候选词、本轮词列表与任务进度相互隔离。词量不足时按当前学科实际可用数量开始；无可用词时明确提示、不启动空任务，不跨学科补齐。'
    } else {
        $body = Replace-Required $body '- [ ] 词库同时有英语和语文时，两科分别发起任务，当前学科明确，所出词语全部属于该学科；词量不足不能跨学科补齐，无可用词时给出明确反馈。' "- [ ] 词库同时有英语和语文时，两科分别发起任务，当前学科明确，候选词、本轮词列表和进度相互隔离，所出词语全部属于该学科。`n- [ ] 当前学科可用词量不足时，按实际数量开始；无词时明确提示、不启动空任务，不用另一学科词语补足或自动改用另一学科。"
    }
    Set-Content -LiteralPath (Join-Path $PSScriptRoot "isolation-$issueNumber.md") -Value $body -Encoding utf8
    Write-Output "Prepared #$issueNumber"
}

