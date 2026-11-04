package notify

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// curlTransport sends requests through Windows' curl.exe. Under WSL's default
// NAT networking, Linux cannot reach a Windows service bound to 127.0.0.1, but
// a Windows program started through interop can. Each request runs one
// curl.exe whose stdout carries the response, so a curl.exe that crashes or is
// killed ends the body with an error and the caller reconnects.
type curlTransport struct {
	// executable skips discovery when set.
	executable string

	mu    sync.Mutex
	found string
}

func (t *curlTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil && request.Body != http.NoBody {
		_ = request.Body.Close()
		return nil, errors.New("curl.exe transport does not send request bodies")
	}
	if request.Method != "" && request.Method != http.MethodGet {
		return nil, fmt.Errorf("curl.exe transport only sends GET, not %s", request.Method)
	}
	config, err := curlConfig(request)
	if err != nil {
		return nil, err
	}
	executable, err := t.locate()
	if err != nil {
		return nil, err
	}
	ctx := request.Context()
	// -q must come first so a curlrc cannot alter the request. The URL and
	// headers, including credentials, arrive on stdin rather than the command
	// line, where any Windows or WSL process could read them.
	command := exec.CommandContext(ctx, executable, "-q", "-sS", "-N", "-i", "-K", "-")
	command.Stdin = strings.NewReader(config)
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start curl.exe: %w", err)
	}
	body := &curlBody{ctx: ctx, command: command, reader: bufio.NewReader(stdout), stderr: stderr}
	response, err := readCurlHead(body.reader, request)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			if waitErr := body.wait(); waitErr != nil {
				return nil, waitErr
			}
			return nil, errors.New("curl.exe exited without a response")
		}
		_ = body.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	response.Body = body
	return response, nil
}

func (t *curlTransport) locate() (string, error) {
	if t.executable != "" {
		return t.executable, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.found != "" {
		return t.found, nil
	}
	path, err := findWindowsCurl()
	if err != nil {
		return "", err
	}
	t.found = path
	return path, nil
}

// findWindowsCurl prefers PATH, which WSL extends with Windows directories, and
// falls back to System32 for systemd services whose PATH omits them.
func findWindowsCurl() (string, error) {
	if path, err := exec.LookPath("curl.exe"); err == nil {
		return path, nil
	}
	matches, _ := filepath.Glob("/mnt/*/[Ww][Ii][Nn][Dd][Oo][Ww][Ss]/[Ss]ystem32/curl.exe")
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && info.Mode().IsRegular() {
			return match, nil
		}
	}
	return "", errors.New("curl.exe was not found on PATH or under /mnt/*/Windows/System32; windowsHost needs WSL interop")
}

func curlConfig(request *http.Request) (string, error) {
	var config strings.Builder
	writeCurlOption(&config, "url", request.URL.String())
	writeCurlOption(&config, "connect-timeout", "10")
	for name, values := range request.Header {
		for _, value := range values {
			if strings.ContainsAny(name+value, "\r\n") {
				return "", fmt.Errorf("header %s contains a line break", name)
			}
			writeCurlOption(&config, "header", name+": "+value)
		}
	}
	return config.String(), nil
}

var curlQuoter = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func writeCurlOption(config *strings.Builder, name, value string) {
	fmt.Fprintf(config, "%s = \"%s\"\n", name, curlQuoter.Replace(value))
}

// readCurlHead parses the status line and headers that -i prints before the
// body, skipping interim 1xx responses.
func readCurlHead(reader *bufio.Reader, request *http.Request) (*http.Response, error) {
	text := textproto.NewReader(reader)
	for {
		line, err := text.ReadLine()
		if err != nil {
			return nil, err
		}
		proto, status, _ := strings.Cut(line, " ")
		major, minor, ok := parseCurlProto(proto)
		codeText, reason, _ := strings.Cut(status, " ")
		code, err := strconv.Atoi(codeText)
		if !ok || err != nil || code < 100 || code > 999 {
			return nil, fmt.Errorf("curl.exe printed a malformed status line %q", line)
		}
		header, err := text.ReadMIMEHeader()
		if err != nil {
			return nil, err
		}
		if code < 200 {
			continue
		}
		response := &http.Response{
			Status:        strings.TrimSpace(codeText + " " + reason),
			StatusCode:    code,
			Proto:         proto,
			ProtoMajor:    major,
			ProtoMinor:    minor,
			Header:        http.Header(header),
			ContentLength: -1,
			Request:       request,
		}
		// curl has already removed the chunked framing from the body it prints.
		response.Header.Del("Transfer-Encoding")
		if length, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64); err == nil {
			response.ContentLength = length
		}
		return response, nil
	}
}

func parseCurlProto(proto string) (int, int, bool) {
	if major, minor, ok := http.ParseHTTPVersion(proto); ok {
		return major, minor, true
	}
	switch proto {
	case "HTTP/2":
		return 2, 0, true
	case "HTTP/3":
		return 3, 0, true
	}
	return 0, 0, false
}

// curlBody streams curl.exe's stdout. When the stream ends, it reaps curl.exe
// and turns a failed exit into the read error, so a crash mid-stream is not
// mistaken for the server closing the connection.
type curlBody struct {
	ctx     context.Context
	command *exec.Cmd
	reader  *bufio.Reader
	stderr  *bytes.Buffer

	once sync.Once
	err  error
}

func (b *curlBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF {
		if waitErr := b.wait(); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

// Close stops curl.exe if it is still running. Killing the WSL side of an
// interop process also ends the Windows process.
func (b *curlBody) Close() error {
	if b.command.Process != nil {
		_ = b.command.Process.Kill()
	}
	_ = b.wait()
	return nil
}

func (b *curlBody) wait() error {
	b.once.Do(func() {
		err := b.command.Wait()
		if err == nil {
			return
		}
		if ctxErr := b.ctx.Err(); ctxErr != nil {
			b.err = ctxErr
			return
		}
		if detail := lastLine(b.stderr.String()); detail != "" {
			b.err = fmt.Errorf("curl.exe %v: %s", err, detail)
			return
		}
		b.err = fmt.Errorf("curl.exe %w", err)
	})
	return b.err
}

func lastLine(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
