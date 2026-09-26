#!/bin/sh
set -eu
mkdir -p /notices/go
cp "$(go env GOROOT)/LICENSE" /notices/Go-LICENSE
go list -m -f '{{if .Dir}}{{.Path}} {{.Dir}}{{end}}' all | while read -r module directory; do
  test -n "$directory" || continue
  target="/notices/go/$module"
  mkdir -p "$target"
  find "$directory" -maxdepth 1 -type f \( -iname 'license*' -o -iname 'copying*' -o -iname 'notice*' -o -iname 'copyright*' \) -exec cp '{}' "$target/" \;
  test -n "$(ls -A "$target")" || { echo "Missing license for $module" >&2; exit 1; }
done
