param([string]$EventName)

$ErrorActionPreference = 'Stop'

try {
  if ($EventName -notin @('SessionStart', 'SubagentStart')) { exit 0 }

  $root = $env:PLUGIN_ROOT
  if ([string]::IsNullOrWhiteSpace($root)) { exit 0 }

  $skillPath = Join-Path $root 'skills\goldilocks\SKILL.md'
  if (-not (Test-Path -LiteralPath $skillPath -PathType Leaf)) { exit 0 }

  $raw = [IO.File]::ReadAllText($skillPath)
  if ($raw.Length -gt 0 -and $raw[0] -eq [char]0xFEFF) {
    $raw = $raw.Substring(1)
  }

  $lines = [regex]::Split($raw, "\r?\n")
  $bodyLines = $lines

  if ($lines.Count -gt 0 -and $lines[0] -eq '---') {
    $closing = -1
    for ($index = 1; $index -lt $lines.Count; $index++) {
      if ($lines[$index] -eq '---') {
        $closing = $index
        break
      }
    }
    if ($closing -lt 0) { exit 0 }
    if ($closing + 1 -ge $lines.Count) { exit 0 }
    $bodyLines = $lines[($closing + 1)..($lines.Count - 1)]
  }

  $body = ($bodyLines -join "`n").Trim()
  if ([string]::IsNullOrWhiteSpace($body)) { exit 0 }

  $payload = @{
    hookSpecificOutput = @{
      hookEventName = $EventName
      additionalContext = $body
    }
  }
  [Console]::Out.Write(($payload | ConvertTo-Json -Compress -Depth 3))
} catch {}

exit 0
