package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/server"
)

type remoteOptions struct {
	baseURL string
	token   string
	timeout time.Duration
}

func (r *runner) runRemote(ctx context.Context, args []string) error {
	options, remaining, err := r.parseRemoteOptions(args)
	if err != nil {
		return err
	}
	if len(remaining) == 0 || remaining[0] == "help" || remaining[0] == "--help" || remaining[0] == "-h" {
		writeRemoteHelp(r.options.Stdout)
		return nil
	}
	client := remoteClient{http: r.options.HTTPClient, options: options, output: r.options.Stdout}
	switch remaining[0] {
	case "projects":
		return client.projects(ctx, remaining[1:])
	case "clone":
		return client.clone(ctx, remaining[1:])
	case "open":
		return client.open(ctx, remaining[1:])
	case "job":
		return client.job(ctx, remaining[1:])
	default:
		return usageError("unknown remote command %q", remaining[0])
	}
}

func (r *runner) parseRemoteOptions(args []string) (remoteOptions, []string, error) {
	result := remoteOptions{timeout: defaultRemoteTimeout}
	helpRequested := hasHelpFlag(args)
	remaining := make([]string, 0, len(args))
	protected := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if protected {
			remaining = append(remaining, arg)
			continue
		}
		if arg == "--" {
			protected = true
			remaining = append(remaining, arg)
			continue
		}
		name, assignedValue, assigned := strings.Cut(arg, "=")
		if name != "--base-url" && name != "--token" && name != "--timeout" {
			remaining = append(remaining, arg)
			continue
		}
		value := assignedValue
		if !assigned {
			if index+1 >= len(args) {
				return remoteOptions{}, nil, usageError("option %s requires a value", name)
			}
			index++
			value = args[index]
		}
		switch name {
		case "--base-url":
			result.baseURL = value
		case "--token":
			result.token = value
		case "--timeout":
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed <= 0 {
				return remoteOptions{}, nil, usageError("--timeout must be a positive duration")
			}
			result.timeout = parsed
		}
	}
	if result.baseURL == "" {
		result.baseURL = strings.TrimSpace(r.options.LookupEnv("OP_REMOTE_URL"))
	}
	if result.baseURL == "" {
		scheme := "http"
		if r.config.Server.TLSCertFile != "" {
			scheme = "https"
		}
		address := r.config.Server.Listen
		if host, port, err := net.SplitHostPort(address); err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		result.baseURL = scheme + "://" + address
	}
	parsed, err := url.Parse(result.baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return remoteOptions{}, nil, usageError("remote base URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	result.baseURL = strings.TrimRight(parsed.String(), "/")
	if result.token == "" {
		result.token = strings.TrimSpace(r.options.LookupEnv("OP_API_TOKEN"))
	}
	if result.token == "" && !helpRequested {
		result.token, err = r.apiToken()
		if err != nil {
			return remoteOptions{}, nil, err
		}
	}
	if strings.TrimSpace(result.token) == "" && !helpRequested {
		return remoteOptions{}, nil, domain.FieldError(domain.ErrorCodeConfig, "cli.remote", "token", "--token, OP_API_TOKEN, or a non-empty server token file is required")
	}
	return result, remaining, nil
}

func remoteConnectionIsExplicit(args []string, lookupEnv func(string) string) bool {
	hasURL := strings.TrimSpace(lookupEnv("OP_REMOTE_URL")) != ""
	hasToken := strings.TrimSpace(lookupEnv("OP_API_TOKEN")) != ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		name, value, assigned := strings.Cut(arg, "=")
		if name != "--base-url" && name != "--token" {
			continue
		}
		if !assigned && index+1 < len(args) {
			index++
			value = args[index]
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		if name == "--base-url" {
			hasURL = true
		} else {
			hasToken = true
		}
	}
	return hasURL && hasToken
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "help" {
			return true
		}
	}
	return false
}

type remoteClient struct {
	http    HTTPClient
	options remoteOptions
	output  io.Writer
}

func (c remoteClient) projects(ctx context.Context, args []string) error {
	usage := func() {
		fmt.Fprintln(c.output, "Usage: op remote projects [--base-url URL] [--token TOKEN] [--timeout DURATION]")
	}
	positionals, err := parseFlags("remote projects", args, nil, usage, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("remote projects accepts no arguments")
	}
	return c.request(ctx, http.MethodGet, "/v1/projects", nil, http.StatusOK)
}

func (c remoteClient) clone(ctx context.Context, args []string) error {
	var directory, profile string
	var open bool
	usage := func() {
		fmt.Fprintln(c.output, "Usage: op remote clone <url> [--directory NAME] [--open] [--profile NAME]")
	}
	positionals, err := parseFlags("remote clone", args, map[string]optionKind{"--directory": valueOption, "--open": boolOption, "--profile": valueOption}, usage, func(set *flag.FlagSet) {
		set.StringVar(&directory, "directory", "", "destination directory name")
		set.BoolVar(&open, "open", false, "open after cloning")
		set.StringVar(&profile, "profile", "", "tmux profile")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return usageError("remote clone requires exactly one repository URL")
	}
	if err := validateProfileOption(profile, open, "cli.remote_clone"); err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, "/v1/projects/clone", domain.CloneRequest{
		URL: positionals[0], Directory: directory, OpenOnFinish: open, Profile: profile,
	}, http.StatusAccepted)
}

func (c remoteClient) open(ctx context.Context, args []string) error {
	var profile string
	var newInstance bool
	usage := func() { fmt.Fprintln(c.output, "Usage: op remote open <project ID> [--profile NAME] [--new-instance]") }
	positionals, err := parseFlags("remote open", args, map[string]optionKind{"--profile": valueOption, "--new-instance": boolOption}, usage, func(set *flag.FlagSet) {
		set.StringVar(&profile, "profile", "", "tmux profile")
		set.BoolVar(&newInstance, "new-instance", false, "create another project window")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return usageError("remote open requires exactly one project ID")
	}
	payload := struct {
		Profile     string `json:"profile,omitempty"`
		NewInstance bool   `json:"newInstance,omitempty"`
	}{Profile: profile, NewInstance: newInstance}
	return c.request(ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(positionals[0])+"/open", payload, http.StatusOK)
}

func (c remoteClient) job(ctx context.Context, args []string) error {
	usage := func() { fmt.Fprintln(c.output, "Usage: op remote job <job ID>") }
	positionals, err := parseFlags("remote job", args, nil, usage, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return usageError("remote job requires exactly one job ID")
	}
	return c.request(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(positionals[0]), nil, http.StatusOK)
}

func (c remoteClient) request(parent context.Context, method, path string, payload any, expectedStatus int) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return domain.NewError(domain.ErrorCodeInternal, "cli.remote", "encode request", err)
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(parent, c.options.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, c.options.baseURL+path, body)
	if err != nil {
		return domain.NewError(domain.ErrorCodeInvalidArgument, "cli.remote", "construct request", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.options.token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return domain.NewError(domain.CodeOf(ctx.Err()), "cli.remote", "remote request did not complete", ctx.Err())
		}
		return domain.NewError(domain.ErrorCodeDependency, "cli.remote", "remote request failed", err)
	}
	defer response.Body.Close()
	const maxResponseBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return domain.NewError(domain.ErrorCodeDependency, "cli.remote", "read remote response", err)
	}
	if len(data) > maxResponseBytes {
		return domain.NewError(domain.ErrorCodeDependency, "cli.remote", "remote response exceeds 4 MiB", nil)
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" || !json.Valid(data) {
		return domain.NewError(domain.ErrorCodeDependency, "cli.remote", fmt.Sprintf("remote server returned HTTP %d with a non-JSON response", response.StatusCode), nil)
	}
	if response.StatusCode != expectedStatus {
		return remoteStatusError(response.StatusCode, data)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, bytes.TrimSpace(data), "", "  "); err != nil {
		return domain.NewError(domain.ErrorCodeDependency, "cli.remote", "format remote JSON response", err)
	}
	formatted.WriteByte('\n')
	_, err = c.output.Write(formatted.Bytes())
	return err
}

func remoteStatusError(status int, data []byte) error {
	var envelope server.ErrorResponse
	message := strings.TrimSpace(string(data))
	code := codeForHTTPStatus(status)
	if json.Unmarshal(data, &envelope) == nil && envelope.Error != nil {
		if envelope.Error.Code != "" {
			code = envelope.Error.Code
		}
		message = envelope.Error.Message
		if envelope.Error.Field != "" {
			message = envelope.Error.Field + ": " + message
		}
	}
	return domain.NewError(code, "cli.remote", "HTTP "+strconv.Itoa(status)+": "+message, nil)
}

func codeForHTTPStatus(status int) domain.ErrorCode {
	switch status {
	case http.StatusBadRequest:
		return domain.ErrorCodeInvalidArgument
	case http.StatusUnauthorized:
		return domain.ErrorCodeUnauthorized
	case http.StatusForbidden:
		return domain.ErrorCodeForbidden
	case http.StatusNotFound:
		return domain.ErrorCodeNotFound
	case http.StatusConflict:
		return domain.ErrorCodeConflict
	case http.StatusRequestTimeout:
		return domain.ErrorCodeCanceled
	case http.StatusGatewayTimeout:
		return domain.ErrorCodeTimeout
	case http.StatusServiceUnavailable:
		return domain.ErrorCodeDependency
	default:
		return domain.ErrorCodeInternal
	}
}

func writeRemoteHelp(writer io.Writer) {
	fmt.Fprint(writer, `Usage: op remote [--base-url URL] [--token TOKEN] [--timeout DURATION] <command>

Commands:
  projects
  clone <url> [--directory NAME] [--open] [--profile NAME]
  open <project ID> [--profile NAME] [--new-instance]
  job <job ID>

Defaults use OP_REMOTE_URL, OP_API_TOKEN, and the configured server listener
and token file. Successful responses are written as JSON.
`)
}
