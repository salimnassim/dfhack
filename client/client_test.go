package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/salimnassim/dfhack/gen/proto"
	"google.golang.org/protobuf/proto"
)

// startFakeServer accepts a single connection on a loopback listener and
// hands it to serverFn. Cleanup waits for serverFn to return, so it may
// safely call t.Error.
func startFakeServer(t *testing.T, serverFn func(conn net.Conn)) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan struct{})
	t.Cleanup(func() {
		ln.Close()
		<-done
	})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serverFn(conn)
	}()

	return ln.Addr().String()
}

// dialFake starts a fake server that performs the handshake then runs
// serverFn, and returns a Client connected to it.
func dialFake(t *testing.T, serverFn func(conn net.Conn, r *bufio.Reader)) *Client {
	t.Helper()

	addr := startFakeServer(t, func(conn net.Conn) {
		r := bufio.NewReader(conn)
		readHandshake(t, r)
		writeHandshake(t, conn, responseMagic, protocolVersion)
		serverFn(conn, r)
	})

	c, err := Dial(t.Context(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	// Closing before the server's cleanup lets a server blocked on read exit.
	t.Cleanup(func() { c.conn.Close() })
	return c
}

func readHandshake(t *testing.T, r io.Reader) {
	t.Helper()

	req := make([]byte, len(requestMagic)+4)
	if _, err := io.ReadFull(r, req); err != nil {
		t.Errorf("server: read handshake request: %v", err)
	}
}

func writeHandshake(t *testing.T, conn net.Conn, magic string, version uint32) {
	t.Helper()

	resp := make([]byte, len(responseMagic)+4)
	copy(resp, magic)
	binary.LittleEndian.PutUint32(resp[len(responseMagic):], version)
	if _, err := conn.Write(resp); err != nil {
		t.Errorf("server: write handshake response: %v", err)
	}
}

func writeFrameRaw(t *testing.T, conn net.Conn, id int16, size uint32, body []byte) {
	t.Helper()

	buf := make([]byte, headerSize+len(body))
	putHeader(buf, id, size)
	copy(buf[headerSize:], body)
	if _, err := conn.Write(buf); err != nil {
		t.Errorf("server: write frame: %v", err)
	}
}

func writeFrame(t *testing.T, conn net.Conn, id int16, body []byte) {
	t.Helper()
	writeFrameRaw(t, conn, id, uint32(len(body)), body)
}

func writeMessage(t *testing.T, conn net.Conn, id int16, msg proto.Message) {
	t.Helper()

	body, err := proto.Marshal(msg)
	if err != nil {
		t.Errorf("server: marshal %T: %v", msg, err)
		return
	}
	writeFrame(t, conn, id, body)
}

func writeFailFrame(t *testing.T, conn net.Conn, code pb.CoreErrorNotification_ErrorCode) {
	t.Helper()
	writeFrameRaw(t, conn, replyFail, uint32(int32(code)), nil)
}

func readFrame(t *testing.T, r *bufio.Reader) (int16, []byte) {
	t.Helper()

	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		t.Errorf("server: read frame header: %v", err)
		return 0, nil
	}
	id := int16(binary.LittleEndian.Uint16(hdr[0:2]))
	body := make([]byte, binary.LittleEndian.Uint32(hdr[4:8]))
	if _, err := io.ReadFull(r, body); err != nil {
		t.Errorf("server: read frame body: %v", err)
		return 0, nil
	}
	return id, body
}

func TestDial_HandshakeSuccess(t *testing.T) {
	dialFake(t, func(net.Conn, *bufio.Reader) {})
}

func TestDial_HandshakeErrors(t *testing.T) {
	tests := []struct {
		name    string
		magic   string
		version uint32
		want    error
	}{
		{name: "bad magic", magic: "BADMAGIC", version: protocolVersion, want: ErrBadMagic},
		{name: "version mismatch", magic: responseMagic, version: 99, want: ErrProtocolVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := startFakeServer(t, func(conn net.Conn) {
				readHandshake(t, conn)
				writeHandshake(t, conn, tt.magic, tt.version)
			})

			_, err := Dial(t.Context(), addr)
			if !errors.Is(err, tt.want) {
				t.Errorf("Dial() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDial_HandshakeTimeout(t *testing.T) {
	release := make(chan struct{})
	addr := startFakeServer(t, func(net.Conn) { <-release })
	defer close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err := Dial(ctx, addr)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Dial() error = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestDial_ConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	if _, err := Dial(t.Context(), addr); err == nil {
		t.Error("Dial() error = nil, want non-nil")
	}
}

func TestCall_Success(t *testing.T) {
	reqCh := make(chan *pb.CoreRunCommandRequest, 1)
	c := dialFake(t, func(conn net.Conn, r *bufio.Reader) {
		id, body := readFrame(t, r)
		if id != RunCommandID {
			t.Errorf("server: call id = %d, want %d", id, RunCommandID)
		}
		req := &pb.CoreRunCommandRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			t.Errorf("server: unmarshal request: %v", err)
		}
		reqCh <- req
		writeMessage(t, conn, replyResult, &pb.EmptyMessage{})
	})

	cmd := &pb.CoreRunCommandRequest{Command: new("ls"), Arguments: []string{"-a"}}
	if err := c.Call(t.Context(), RunCommandID, cmd, &pb.EmptyMessage{}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	got := <-reqCh
	if !proto.Equal(got, cmd) {
		t.Errorf("server received %v, want %v", got, cmd)
	}
}

func TestCall_ReplyFail(t *testing.T) {
	c := dialFake(t, func(conn net.Conn, r *bufio.Reader) {
		readFrame(t, r)
		writeFailFrame(t, conn, pb.CoreErrorNotification_CR_NOT_FOUND)
		// The stream is still in sync after a failure reply.
		readFrame(t, r)
		writeMessage(t, conn, replyResult, &pb.EmptyMessage{})
	})

	err := c.Call(t.Context(), RunCommandID, &pb.CoreRunCommandRequest{Command: new("x")}, &pb.EmptyMessage{})
	rpcErr, ok := errors.AsType[*RPCError](err)
	if !ok {
		t.Fatalf("Call() error = %v, want *RPCError", err)
	}
	if want := pb.CoreErrorNotification_CR_NOT_FOUND; rpcErr.Code != want {
		t.Errorf("RPCError.Code = %v, want %v", rpcErr.Code, want)
	}

	if err := c.Call(t.Context(), RunCommandID, &pb.CoreRunCommandRequest{Command: new("x")}, &pb.EmptyMessage{}); err != nil {
		t.Errorf("Call() after RPCError = %v, want nil", err)
	}
}

func TestCall_TextNotificationThenResult(t *testing.T) {
	c := dialFake(t, func(conn net.Conn, r *bufio.Reader) {
		readFrame(t, r)
		note := &pb.CoreTextNotification{
			Fragments: []*pb.CoreTextFragment{{Text: new("shig\xa2s")}},
		}
		writeMessage(t, conn, replyText, note)
		writeMessage(t, conn, replyResult, &pb.EmptyMessage{})
	})

	var received []string
	c.OnText = func(n *pb.CoreTextNotification) {
		for _, f := range n.GetFragments() {
			received = append(received, f.GetText())
		}
	}

	if err := c.Call(t.Context(), RunCommandID, &pb.CoreRunCommandRequest{Command: new("x")}, &pb.EmptyMessage{}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if want := []string{"shigós"}; !slices.Equal(received, want) {
		t.Errorf("OnText received %q, want %q", received, want)
	}
}

func TestCall_BrokenStream(t *testing.T) {
	tests := []struct {
		name   string
		server func(t *testing.T, conn net.Conn)
		want   error
	}{
		{
			name:   "unexpected reply id",
			server: func(t *testing.T, conn net.Conn) { writeFrame(t, conn, 42, []byte("junk")) },
			want:   ErrUnexpectedReply,
		},
		{
			name:   "reply size out of range",
			server: func(t *testing.T, conn net.Conn) { writeFrameRaw(t, conn, replyResult, 0xFFFFFFFF, nil) },
			want:   ErrFrameTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := dialFake(t, func(conn net.Conn, r *bufio.Reader) {
				readFrame(t, r)
				tt.server(t, conn)
			})

			req := &pb.CoreRunCommandRequest{Command: new("foo")}
			if err := c.Call(t.Context(), RunCommandID, req, &pb.EmptyMessage{}); !errors.Is(err, tt.want) {
				t.Errorf("Call() error = %v, want %v", err, tt.want)
			}
			// The server sends nothing more; a second Call must fail without I/O.
			if err := c.Call(t.Context(), RunCommandID, req, &pb.EmptyMessage{}); !errors.Is(err, tt.want) {
				t.Errorf("second Call() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCall_ContextCanceled(t *testing.T) {
	c := dialFake(t, func(conn net.Conn, r *bufio.Reader) {
		readFrame(t, r)
		// Never reply; wait for the client to hang up.
		io.Copy(io.Discard, r)
	})

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	err := c.Call(ctx, RunCommandID, &pb.CoreRunCommandRequest{Command: new("x")}, &pb.EmptyMessage{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Call() error = %v, want %v", err, context.Canceled)
	}
	if err := c.Call(t.Context(), RunCommandID, &pb.CoreRunCommandRequest{Command: new("x")}, &pb.EmptyMessage{}); err == nil {
		t.Error("Call() after cancellation = nil, want error")
	}
}

func TestBind(t *testing.T) {
	tests := []struct {
		name   string
		plugin string
	}{
		{name: "plugin method", plugin: "someplugin"},
		{name: "core method", plugin: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqCh := make(chan *pb.CoreBindRequest, 1)
			c := dialFake(t, func(conn net.Conn, r *bufio.Reader) {
				id, body := readFrame(t, r)
				if id != bindMethodID {
					t.Errorf("server: call id = %d, want %d", id, bindMethodID)
				}
				req := &pb.CoreBindRequest{}
				if err := proto.Unmarshal(body, req); err != nil {
					t.Errorf("server: unmarshal bind request: %v", err)
				}
				reqCh <- req
				writeMessage(t, conn, replyResult, &pb.CoreBindReply{AssignedId: new(int32(7))})
			})

			id, err := c.Bind(t.Context(), "SomeMethod", tt.plugin, &pb.EmptyMessage{}, &pb.StringMessage{})
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}
			if id != 7 {
				t.Errorf("Bind() = %d, want 7", id)
			}

			want := &pb.CoreBindRequest{
				Method:    new("SomeMethod"),
				InputMsg:  new("dfproto.EmptyMessage"),
				OutputMsg: new("dfproto.StringMessage"),
			}
			if tt.plugin != "" {
				want.Plugin = new(tt.plugin)
			}
			if got := <-reqCh; !proto.Equal(got, want) {
				t.Errorf("server received %v, want %v", got, want)
			}
		})
	}
}

func TestBind_Error(t *testing.T) {
	c := dialFake(t, func(conn net.Conn, r *bufio.Reader) {
		readFrame(t, r)
		writeFailFrame(t, conn, pb.CoreErrorNotification_CR_NOT_IMPLEMENTED)
	})

	_, err := c.Bind(t.Context(), "SomeMethod", "", &pb.EmptyMessage{}, &pb.EmptyMessage{})
	if _, ok := errors.AsType[*RPCError](err); !ok {
		t.Fatalf("Bind() error = %v, want *RPCError", err)
	}
	if !strings.Contains(err.Error(), `bind "SomeMethod"`) {
		t.Errorf("Bind() error = %v, missing bind context", err)
	}
}

func TestClose_SendsQuitFrame(t *testing.T) {
	quitCh := make(chan int16, 1)
	c := dialFake(t, func(_ net.Conn, r *bufio.Reader) {
		id, _ := readFrame(t, r)
		quitCh <- id
	})

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if id := <-quitCh; id != requestQuit {
		t.Errorf("server received id %d, want %d", id, requestQuit)
	}
}

func TestDecodeCP437(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "passthrough", in: "urist", want: "urist"},
		{name: "e acute", in: "\x82", want: "é"},
		{name: "o acute", in: "\xa2", want: "ó"},
		{name: "mixed", in: "shig\xa2s", want: "shigós"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeCP437(tt.in); got != tt.want {
				t.Errorf("decodeCP437(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
