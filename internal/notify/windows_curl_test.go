package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCurl stands in for curl.exe. Invocation n follows the mode in mode-n,
// falling back to mode, and records its arguments and stdin.
const fakeCurl = `#!/bin/sh
dir=$(dirname "$0")
n=$(($(cat "$dir/count" 2>/dev/null || echo 0) + 1))
echo "$n" > "$dir/count"
printf '%s\n' "$@" > "$dir/args-$n"
cat > "$dir/stdin-$n"
mode=$(cat "$dir/mode-$n" 2>/dev/null || cat "$dir/mode")
event='data: {"type":"session.execution.started","data":{"sessionID":"s'$n'"}}'
case "$mode" in
ok)
	printf 'HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n{"data":{}}'
	;;
refused)
	echo 'curl: (7) Failed to connect to 127.0.0.1 port 28894 after 2045 ms: Could not connect to server' >&2
	exit 7
	;;
crash)
	printf 'HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%s\n\n' "$event"
	echo 'curl: (56) Recv failure: Connection was reset' >&2
	exit 56
	;;
hang)
	printf 'HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n%s\n\n' "$event"
	exec sleep 60
	;;
esac
`

func newFakeCurl(t *testing.T, modes ...string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake curl.exe is a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "curl.exe")
	if err := os.WriteFile(script, []byte(fakeCurl), 0o755); err != nil {
		t.Fatal(err)
	}
	for index, mode := range modes {
		name := "mode"
		if len(modes) > 1 {
			name = "mode-" + string(rune('1'+index))
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(mode), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return script, dir
}

func readFakeCurlFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCurlTransportParsesResponseAndKeepsSecretsOffTheCommandLine(t *testing.T) {
	script, dir := newFakeCurl(t, "ok")
	client := &http.Client{Transport: &curlTransport{executable: script}}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:28894/api/session/active", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("opencode", "s3cret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Status != "200 OK" || string(body) != `{"data":{}}` {
		t.Fatalf("response = %d %q body %q", response.StatusCode, response.Status, body)
	}
	if response.Header.Get("Transfer-Encoding") != "" || response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", response.Header)
	}
	args := readFakeCurlFile(t, dir, "args-1")
	if !strings.HasPrefix(args, "-q\n") || strings.Contains(args, "s3cret") || strings.Contains(args, "28894") {
		t.Fatalf("command line = %q, want -q first and no URL or credentials", args)
	}
	stdin := readFakeCurlFile(t, dir, "stdin-1")
	authorization := request.Header.Get("Authorization")
	if !strings.Contains(stdin, `url = "http://127.0.0.1:28894/api/session/active"`) || !strings.Contains(stdin, `header = "Authorization: `+authorization+`"`) {
		t.Fatalf("stdin config = %q", stdin)
	}
}

func TestCurlTransportReportsCurlFailures(t *testing.T) {
	script, _ := newFakeCurl(t, "refused")
	client := &http.Client{Transport: &curlTransport{executable: script}}
	_, err := client.Get("http://127.0.0.1:28894/api/event")
	if err == nil || !strings.Contains(err.Error(), "Failed to connect") || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("error = %v, want curl's own message and exit status", err)
	}
}

func TestCurlTransportReportsACrashMidStream(t *testing.T) {
	script, _ := newFakeCurl(t, "crash")
	client := &http.Client{Transport: &curlTransport{executable: script}}
	response, err := client.Get("http://127.0.0.1:28894/api/event")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var events []string
	err = readSSE(response.Body, func(data string) { events = append(events, data) })
	if len(events) != 1 || err == nil || !strings.Contains(err.Error(), "Recv failure") {
		t.Fatalf("events %v, error %v; want the event before the crash, then curl's error", events, err)
	}
}

func TestOpenCode2WatchReconnectsWhenCurlCrashes(t *testing.T) {
	script, _ := newFakeCurl(t, "refused", "crash", "hang")
	logs := &lockedBuffer{}
	client := newOpenCode2Client(OpenCode2Config{BaseURL: "http://127.0.0.1:28894", Password: "pw", WindowsHost: true}, slog.New(slog.NewTextHandler(logs, nil)))
	client.client.Transport = &curlTransport{executable: script}
	client.minDelay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- client.watch(ctx, func(_ string, payload json.RawMessage) {
			var event struct {
				Properties struct {
					SessionID string `json:"sessionID"`
				} `json:"properties"`
			}
			_ = json.Unmarshal(payload, &event)
			sessions <- event.Properties.SessionID
		})
	}()
	for _, want := range []string{"s2", "s3"} {
		select {
		case got := <-sessions:
			if got != want {
				t.Fatalf("event from session %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no event from %s; logs:\n%s", want, logs)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not return after cancel; the hanging curl.exe was not stopped")
	}
	disconnects := regexp.MustCompile(`msg="opencode2 sse disconnected" err="((?:[^"\\]|\\.)*)" reconnect=(\S+)`).FindAllStringSubmatch(logs.String(), -1)
	if len(disconnects) != 2 {
		t.Fatalf("disconnect logs = %q", disconnects)
	}
	if !strings.Contains(disconnects[0][1], "Failed to connect") || disconnects[0][2] != "20ms" {
		t.Fatalf("first disconnect = %q", disconnects[0])
	}
	// The failed attempt doubled the delay; the accepted stream must reset it.
	if !strings.Contains(disconnects[1][1], "Recv failure") || disconnects[1][2] != "20ms" {
		t.Fatalf("crash disconnect = %q, want curl's error and a reset 20ms delay", disconnects[1])
	}
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
