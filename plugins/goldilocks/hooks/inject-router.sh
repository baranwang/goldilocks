#!/bin/sh

event=${1-}
case "$event" in
  SessionStart|SubagentStart) ;;
  *) exit 0 ;;
esac

root=${PLUGIN_ROOT-}
[ -n "$root" ] || exit 0
skill=$root/skills/goldilocks/SKILL.md
[ -r "$skill" ] || exit 0

LC_ALL=C awk -v event="$event" '
function escape_json(value, output, position, char) {
  output = ""
  for (position = 1; position <= length(value); position++) {
    char = substr(value, position, 1)
    if (char == "\\") output = output "\\\\"
    else if (char == "\"") output = output "\\\""
    else if (char == "\t") output = output "\\t"
    else if (char ~ /[[:cntrl:]]/) invalid = 1
    else output = output char
  }
  return output
}
{
  sub(/\r$/, "", $0)
}
NR == 1 {
  sub(/^\357\273\277/, "", $0)
}
NR == 1 && $0 == "---" {
  opened = 1
  in_frontmatter = 1
  next
}
in_frontmatter && $0 == "---" {
  closed = 1
  in_frontmatter = 0
  next
}
in_frontmatter {
  next
}
{
  lines[++count] = $0
}
END {
  if (opened && !closed) exit 0

  start = 1
  while (start <= count && lines[start] ~ /^[[:space:]]*$/) start++
  end = count
  while (end >= start && lines[end] ~ /^[[:space:]]*$/) end--
  if (end < start) exit 0

  sub(/^[[:space:]]+/, "", lines[start])
  sub(/[[:space:]]+$/, "", lines[end])

  body = ""
  for (position = start; position <= end; position++) {
    if (position > start) body = body "\\n"
    body = body escape_json(lines[position])
  }
  if (invalid) exit 0

  printf "{\"hookSpecificOutput\":{\"hookEventName\":\"%s\",\"additionalContext\":\"%s\"}}", escape_json(event), body
}
' "$skill" 2>/dev/null || exit 0

exit 0
