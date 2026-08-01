package wslproxy

// ResolverScript is fixed non-login shell source. The requested executable
// and every user argument are supplied separately as positional parameters.
const ResolverScript = `
marker='` + BinaryMarker + `'
validate_candidate() {
    checked=$1
    validation_error=
    case "$checked" in
        /*) ;;
        *) validation_error='path is not absolute'; return 1 ;;
    esac
    case "$checked" in
        *.[eE][xX][eE]) validation_error='path has a Windows .exe suffix'; return 1 ;;
    esac
    if [ ! -f "$checked" ] || [ ! -x "$checked" ]; then
        validation_error='path is not an executable file'
        return 1
    fi
    magic=$(LC_ALL=C od -An -tx1 -N4 "$checked" 2>/dev/null | LC_ALL=C tr -d ' \n')
    case "$magic" in
        4d5a*) validation_error='file is a Windows PE binary'; return 1 ;;
    esac
    if [ "$magic" != 7f454c46 ]; then
        validation_error='file is not an ELF binary'
        return 1
    fi
    if ! LC_ALL=C grep -a -F -q "$marker" "$checked" 2>/dev/null; then
        validation_error='ELF does not contain this project marker'
        return 1
    fi
    return 0
}

requested=$1
shift
candidate=
if [ -n "$requested" ]; then
    if ! validate_candidate "$requested"; then
        printf '%s\n' "error: wsl proxy: OP_WSL_OP is invalid: $validation_error" >&2
        exit 126
    fi
    candidate=$requested
else
    path_candidate=$(command -v op 2>/dev/null) || path_candidate=
    for checked in "$path_candidate" "$HOME/.local/bin/op" /usr/local/bin/op /usr/bin/op; do
        if [ -n "$checked" ] && validate_candidate "$checked"; then
            candidate=$checked
            break
        fi
    done
    if [ -z "$candidate" ]; then
        printf '%s\n' 'error: wsl proxy: this project Linux op ELF was not found in PATH or common install locations; install it in WSL or set OP_WSL_OP' >&2
        exit 127
    fi
fi
exec "$candidate" "$@"
`
