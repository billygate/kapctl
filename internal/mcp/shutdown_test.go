package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// runToEOF runs a server over an IOTransport whose reader yields `input`
// then EOF, mimicking a stdio client that sends something and disconnects.
func runToEOF(t *testing.T, input string) error {
	t.Helper()
	srv := NewServer(newTestDeps(true), Options{})
	r := io.NopCloser(bytes.NewReader([]byte(input)))
	w := nopWriteCloser{&bytes.Buffer{}}
	return srv.Run(context.Background(), &mcp.IOTransport{Reader: r, Writer: w})
}

func TestIsCleanShutdown(t *testing.T) {
	if !IsCleanShutdown(nil) {
		t.Error("nil error must be a clean shutdown")
	}
	if IsCleanShutdown(errors.New("boom")) {
		t.Error("a plain error must not be a clean shutdown")
	}
}

// A client that sends a request and then closes the pipe makes the SDK
// return its "server is closing" error; the command must treat that as a
// clean exit rather than a failure.
func TestRunDisconnectMidRequestIsClean(t *testing.T) {
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"t","version":"1"}}}` + "\n"
	err := runToEOF(t, init)
	if !IsCleanShutdown(err) {
		t.Fatalf("mid-request disconnect should be clean, got %v", err)
	}
}
