# severity: hard
10-go() {
  if ! command -v mise >/dev/null 2>&1; then
    return $(fail "install mise from https://mise.jdx.dev/getting-started.html")
  fi
  mise install --locked || return $(fail "mise install --locked failed")
  shims="$(mise data-dir 2>/dev/null || echo "$HOME/.local/share/mise")/shims"
  case ":$PATH:" in
    *":$shims:"*) ;;
    *) export PATH="$shims:$PATH" ;;
  esac

  info "validating approved specs"
  yass validate .
  yass lint .

  info "checking normalized and generated OpenAPI output"
  script/check-generated

  info "checking conventional commit history behavior"
  script/test-conventional-commits

  info "checking formatting, vet, tests, and build"
  unformatted="$(find . -type f -name '*.go' -not -path './.git/*' -print0 | xargs -0 gofmt -l)"
  if [ -n "$unformatted" ]; then
    printf '%s\n' "$unformatted" >&2
    return $(fail "Go files need gofmt")
  fi
  go vet ./...
  go test ./...
  go build ./...
  pass "specs, generation, format, vet, tests, and build"
}
