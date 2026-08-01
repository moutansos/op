package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/moutansos/op/internal/config"
)

var errHelp = errors.New("help requested")

type cliUsageError struct{ message string }

func (e *cliUsageError) Error() string { return e.message }

func usageError(format string, values ...any) error {
	return &cliUsageError{message: fmt.Sprintf(format, values...)}
}

func parseGlobals(args []string) (globalFlags, []string, error) {
	path, remaining, err := config.ExtractConfigPath(args)
	if err != nil {
		return globalFlags{}, nil, err
	}
	result := globalFlags{configPath: path}
	filtered := make([]string, 0, len(remaining))
	protected := false
	for _, arg := range remaining {
		if protected {
			filtered = append(filtered, arg)
			continue
		}
		if arg == "--" {
			protected = true
			filtered = append(filtered, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--no-target", "--no-repo-update":
			enabled := true
			if hasValue {
				parsed, parseErr := strconv.ParseBool(value)
				if parseErr != nil {
					return globalFlags{}, nil, usageError("%s requires a boolean value", name)
				}
				enabled = parsed
			}
			if name == "--no-target" {
				result.noTarget = enabled
			} else {
				result.noRepoUpdate = enabled
			}
		default:
			filtered = append(filtered, arg)
		}
	}
	return result, filtered, nil
}

type optionKind uint8

const (
	boolOption optionKind = iota
	valueOption
)

func parseFlags(name string, args []string, options map[string]optionKind, usage func(), configure func(*flag.FlagSet)) ([]string, error) {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	configure(set)
	reordered, err := reorderOptions(args, options)
	if err != nil {
		return nil, err
	}
	if err := set.Parse(reordered); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage()
			return nil, errHelp
		}
		return nil, usageError("%s", err)
	}
	return set.Args(), nil
}

func reorderOptions(args []string, options map[string]optionKind) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	protected := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			protected = true
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if arg == "-h" || arg == "--help" {
			flags = append(flags, arg)
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		name, _, assigned := strings.Cut(arg, "=")
		kind, ok := options[name]
		if !ok {
			return nil, usageError("unknown option %q", name)
		}
		flags = append(flags, arg)
		if kind == valueOption && !assigned {
			if i+1 >= len(args) {
				return nil, usageError("option %s requires a value", name)
			}
			i++
			flags = append(flags, args[i])
		}
	}
	if protected {
		flags = append(flags, "--")
	}
	return append(flags, positionals...), nil
}
