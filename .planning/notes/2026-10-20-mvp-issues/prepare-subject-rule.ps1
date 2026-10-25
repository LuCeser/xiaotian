$ErrorActionPreference = 'Stop'
$ruleText = (Get-Content (Join-Path $PSScriptRoot 'subject-rule.md') -Raw).TrimEnd()
function Replace-Required([string]$body, [string]$old, [string]$new) {
    if (-not $body.Contains($old)) { throw "Expected text not found: $old" }
    return $body.Replace($old, $new)
}
foreach ($issueNumber in @(55,1,58)) {
    $before = Get-Content (Join-Path $PSScriptRoot "subject-rule-before-$issueNumber.json") -Raw | ConvertFrom-Json
    $body = $before.body
    if ($issueNumber -eq 55) {
        $body = Replace-Required $body '本 Issue 尚未在当前版本重新复现；验收条件为待分诊草案，原因未确认。' '本 Issue 尚未在当前版本重新复现，原因未确认；学科／类别规则已由用户确认，按 ready-for-agent 处理。'
        $body = Replace-Required $body "### 期望行为`n`n一次默写仅使用一个学科的内容。用户可以先完成英语默写，再单独发起语文默写。" ("### 期望行为`n`n一次默写仅使用一个学科的内容。用户可以先完成英语默写，再单独发起语文默写。`n`n" + $ruleText)
        $body = Replace-Required $body '- 发起默写时如何确定学科。' '- 默认由大模型判断学科／类别，并使用户的明确指定或更改优先生效。'
        $body = Replace-Required $body "### 建议验收条件`n`n" "### 验收条件`n`n- [ ] 用户未指定类别时，由大模型判断学科／类别，按判断结果组织单一学科的默写，不强制先手动选择。`n- [ ] 用户明确指定或更改学科／类别时，以用户指定为准；即使与模型判断不同，也不被静默改回。`n"
        $body = Replace-Required $body "- 未指定学科时，是询问用户、使用已选学科，还是采用其他规则？`n" ''
        $body = Replace-Required $body '### 待确认' '### 待复现信息与剩余规则'
    } elseif ($issueNumber -eq 1) {
        $body = Replace-Required $body '| #55 | 学科隔离 | 一轮只出当前学科词语，不跨学科补足、不混入诗文 |' '| #55 | 学科隔离与默认类别判断 | 默认由大模型判断，用户明确指定／更改优先；一轮只出最终学科词语，不跨学科补足、不混入诗文 |'
        $body = Replace-Required $body '- 复用现有学科选择与词列表能力；无可用词时明确反馈，数量不足不跨学科补齐。' '- 学科／类别默认由大模型根据内容及用户意图判断；用户明确指定或更改时，以用户指定为准，不强制每次手选。词列表按最终学科筛选；无可用词时明确反馈，数量不足不跨学科补齐。'
    } else {
        $body = Replace-Required $body '- 发起默写时确定一个学科和本次词列表，正常播报、逐词推进、最后完成；不能把另一学科词语或诗文混入。' '- 发起默写时默认由大模型根据内容及用户意图判断学科／类别；用户明确指定或更改时优先采用用户指定，不强制每次手选。按最终学科确定本次词列表，正常播报、逐词推进、最后完成；不能把另一学科词语或诗文混入。'
        $body = Replace-Required $body "## 验收条件`n`n" "## 验收条件`n`n- [ ] 未明确指定类别时，默认由大模型判断并发起对应学科的默写；用户明确指定或更改类别时，即使与模型判断不同也以用户指定为准，不被静默改回。两种情况下均保持单一学科出题。`n"
    }
    Set-Content -LiteralPath (Join-Path $PSScriptRoot "subject-rule-$issueNumber.md") -Value $body -Encoding utf8
    Write-Output "Prepared #$issueNumber"
}
