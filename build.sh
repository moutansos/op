#!/usr/bin/env bash

set -uo pipefail

SCRIPT_ROOT="$(cd "$(dirname "$0")" && pwd)"
BUILD_OUTPUT_DIR="$SCRIPT_ROOT/.build_output"
LINUX_BINARY="$BUILD_OUTPUT_DIR/op"
WINDOWS_AMD64_BINARY="$BUILD_OUTPUT_DIR/op-windows-amd64.exe"
WINDOWS_ARM64_BINARY="$BUILD_OUTPUT_DIR/op-windows-arm64.exe"

build_flag="false"
windows_flag="false"
test_flag="false"
race_flag="false"
integration_flag="false"
run_flag="false"
all_flag="false"
clean_flag="false"
quiet_flag="false"
linux_binary_ready="false"
run_args=()

function print_usage {
  cat <<EOF
Usage: $0 [options] [-- op arguments]

Options:
  --build                Build the Linux/WSL op binary
  --build-windows        Build Windows amd64 and arm64 WSL proxies
  --test                 Run all unit tests
  --race                 Run all tests with the race detector
  --integration          Run isolated real-tmux integration tests
  --run                  Build and run the Linux/WSL op binary
  --all                  Run build, test, and run
  --clean                Remove .build_output
  -q, --quiet            Suppress stage banners (useful for wrapper scripts)
  --                     Pass remaining arguments to op (requires --run or --all)
  -h, --help             Show this help message

Stage banners and build notices are written to stderr; only op's own output
goes to stdout, so piping machine-readable output is safe.

Examples:
  $0 --build --test
  $0 --build-windows
  $0 --integration
  $0 --run -- projects --json
  $0 --quiet --run -- --config ./config.json open my-project

Build metadata can be overridden with VERSION, COMMIT, and BUILD_DATE.
EOF
}

function print_stage {
  local stage_name="$1"
  if [[ "$quiet_flag" == "true" ]]; then
    return 0
  fi
  # Banners are diagnostics: keep stdout clean so `--run -- projects --json`
  # and other machine-readable output can be piped safely.
  printf '\n==========================================\n' >&2
  printf 'Starting stage: %s\n' "$stage_name" >&2
  printf '==========================================\n\n' >&2
}

function init_output_dir {
  mkdir -p "$BUILD_OUTPUT_DIR"
}

function build_metadata {
  build_version="${VERSION:-dev}"
  build_commit="${COMMIT:-$(git -C "$SCRIPT_ROOT" rev-parse --short HEAD 2>/dev/null || printf unknown)}"
  build_date="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
  build_ldflags="-X main.version=$build_version -X main.commit=$build_commit -X main.date=$build_date"
}

function build_linux {
  init_output_dir
  build_metadata
  (
    cd "$SCRIPT_ROOT" || exit 1
    go build -trimpath -ldflags "$build_ldflags" -o "$LINUX_BINARY" ./cmd/op
  )
  local exit_code=$?
  if [[ "$exit_code" -eq 0 ]]; then
    linux_binary_ready="true"
  fi
  return "$exit_code"
}

function build_windows {
  init_output_dir
  build_metadata
  (
    cd "$SCRIPT_ROOT" || exit 1
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
      go build -trimpath -ldflags "$build_ldflags" -o "$WINDOWS_AMD64_BINARY" ./cmd/op &&
      CGO_ENABLED=0 GOOS=windows GOARCH=arm64 \
        go build -trimpath -ldflags "$build_ldflags" -o "$WINDOWS_ARM64_BINARY" ./cmd/op
  )
}

function run_in_project {
  (
    cd "$SCRIPT_ROOT" || exit 1
    "$@"
  )
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --build) build_flag="true" ;;
    --build-windows) windows_flag="true" ;;
    --test) test_flag="true" ;;
    --race) race_flag="true" ;;
    --integration) integration_flag="true" ;;
    --run) run_flag="true" ;;
    --all) all_flag="true" ;;
    --clean) clean_flag="true" ;;
    -q|--quiet) quiet_flag="true" ;;
    --)
      shift
      run_args=("$@")
      break
      ;;
    -h|--help)
      print_usage
      exit 0
      ;;
    *)
      printf 'Unknown parameter: %s\n\n' "$1" >&2
      print_usage >&2
      exit 2
      ;;
  esac
  shift
done

if [[ "$all_flag" == "true" ]]; then
  build_flag="true"
  test_flag="true"
  run_flag="true"
fi

if [[ "${#run_args[@]}" -gt 0 && "$run_flag" != "true" ]]; then
  printf 'op arguments require --run or --all\n\n' >&2
  print_usage >&2
  exit 2
fi

if [[ "$build_flag" != "true" && "$windows_flag" != "true" && "$test_flag" != "true" && \
      "$race_flag" != "true" && "$integration_flag" != "true" && "$run_flag" != "true" && \
      "$clean_flag" != "true" ]]; then
  print_usage
  exit 0
fi

if [[ "$clean_flag" == "true" ]]; then
  print_stage "Cleaning Build Output"
  rm -rf "$BUILD_OUTPUT_DIR"
fi

if [[ "$build_flag" == "true" ]]; then
  print_stage "Building Linux/WSL Application"
  build_linux || exit $?
  printf 'Built %s\n' "$LINUX_BINARY" >&2
fi

if [[ "$windows_flag" == "true" ]]; then
  print_stage "Building Windows WSL Proxies"
  build_windows || exit $?
  printf 'Built %s\n' "$WINDOWS_AMD64_BINARY" >&2
  printf 'Built %s\n' "$WINDOWS_ARM64_BINARY" >&2
fi

if [[ "$test_flag" == "true" ]]; then
  print_stage "Running Unit Tests"
  run_in_project go test -count=1 ./... || exit $?
fi

if [[ "$race_flag" == "true" ]]; then
  print_stage "Running Race Tests"
  run_in_project go test -race -count=1 ./... || exit $?
fi

if [[ "$integration_flag" == "true" ]]; then
  print_stage "Running Isolated Tmux Integration Tests"
  run_in_project env OP_TMUX_INTEGRATION=1 TMUX= TMUX_PANE= \
    go test -count=1 ./internal/tmux -run '^TestIntegration' -v || exit $?
fi

if [[ "$run_flag" == "true" ]]; then
  print_stage "Running Application"
  if [[ "$linux_binary_ready" != "true" ]]; then
    build_linux || exit $?
  fi
  run_in_project "$LINUX_BINARY" "${run_args[@]}"
  exit $?
fi
