package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/salimnassim/dfhack/gen/proto"
	"google.golang.org/protobuf/proto"
)

func startFakeServer(t *testing.T, serverFn func(conn net.Conn)) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serverFn(conn)
	}()

	return ln.Addr().String()
}

func serverHandshake(t *testing.T, conn net.Conn) {
	t.Helper()

	req := make([]byte, len(requestMagic)+4)
	if _, err := io.ReadFull(conn, req); err != nil {
		t.Errorf("server: read handshake request: %v", err)
		return
	}

	resp := make([]byte, len(responseMagic)+4)
	copy(resp, responseMagic)
	binary.LittleEndian.PutUint32(resp[len(responseMagic):], protocolVersion)
	if _, err := conn.Write(resp); err != nil {
		t.Errorf("server: write handshake response: %v", err)
	}
}

func writeFrame(t *testing.T, conn net.Conn, id int16, body []byte) {
	t.Helper()

	var hdr [headerSize]byte
	binary.LittleEndian.PutUint16(hdr[0:2], uint16(id))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(body)))
	if _, err := conn.Write(hdr[:]); err != nil {
		t.Errorf("server: write frame header: %v", err)
		return
	}
	if len(body) > 0 {
		if _, err := conn.Write(body); err != nil {
			t.Errorf("server: write frame body: %v", err)
		}
	}
}

func writeFrameRaw(t *testing.T, conn net.Conn, id int16, size uint32, body []byte) {
	t.Helper()

	var hdr [headerSize]byte
	binary.LittleEndian.PutUint16(hdr[0:2], uint16(id))
	binary.LittleEndian.PutUint32(hdr[4:8], size)
	if _, err := conn.Write(hdr[:]); err != nil {
		t.Errorf("server: write frame header: %v", err)
		return
	}
	if len(body) > 0 {
		if _, err := conn.Write(body); err != nil {
			t.Errorf("server: write frame body: %v", err)
		}
	}
}

func writeFailFrame(t *testing.T, conn net.Conn, code pb.CoreErrorNotification_ErrorCode) {
	t.Helper()

	id := replyFail
	var hdr [headerSize]byte
	binary.LittleEndian.PutUint16(hdr[0:2], uint16(id))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(int32(code)))
	if _, err := conn.Write(hdr[:]); err != nil {
		t.Errorf("server: write fail frame: %v", err)
	}
}

func readFrame(t *testing.T, r *bufio.Reader) (int16, []byte) {
	t.Helper()

	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		t.Errorf("server: read frame header: %v", err)
		return 0, nil
	}
	id := int16(binary.LittleEndian.Uint16(hdr[0:2]))
	size := binary.LittleEndian.Uint32(hdr[4:8])
	body := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			t.Errorf("server: read frame body: %v", err)
			return 0, nil
		}
	}
	return id, body
}

func TestDial_HandshakeSuccess(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()
}

func TestDial_BadMagic(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		req := make([]byte, len(requestMagic)+4)
		if _, err := io.ReadFull(conn, req); err != nil {
			t.Errorf("server: read handshake request: %v", err)
			return
		}
		resp := make([]byte, len(responseMagic)+4)
		copy(resp, "BADMAGIC")
		binary.LittleEndian.PutUint32(resp[8:], protocolVersion)
		conn.Write(resp)
	})

	_, err := Dial(context.Background(), addr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad handshake magic") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDial_VersionMismatch(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		req := make([]byte, len(requestMagic)+4)
		if _, err := io.ReadFull(conn, req); err != nil {
			t.Errorf("server: read handshake request: %v", err)
			return
		}
		resp := make([]byte, len(responseMagic)+4)
		copy(resp, responseMagic)
		binary.LittleEndian.PutUint32(resp[len(responseMagic):], 99)
		conn.Write(resp)
	})

	_, err := Dial(context.Background(), addr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDial_ConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	_, err = Dial(context.Background(), addr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCall_Success(t *testing.T) {
	reqCh := make(chan *pb.CoreRunCommandRequest, 1)
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)

		id, body := readFrame(t, r)
		if id != RunCommandID {
			t.Errorf("call id = %d, want %d", id, RunCommandID)
		}
		req := &pb.CoreRunCommandRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			t.Errorf("server: unmarshal request: %v", err)
		}
		reqCh <- req

		out, _ := proto.Marshal(&pb.EmptyMessage{})
		writeFrame(t, conn, replyResult, out)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()

	cmd := &pb.CoreRunCommandRequest{Command: new("ls"), Arguments: []string{"-a"}}
	out := &pb.EmptyMessage{}
	if err := c.Call(RunCommandID, cmd, out); err != nil {
		t.Fatalf("Call: %v", err)
	}

	select {
	case got := <-reqCh:
		if got.GetCommand() != "ls" {
			t.Errorf("command = %q, want ls", got.GetCommand())
		}
		if want := []string{"-a"}; len(got.GetArguments()) != 1 || got.GetArguments()[0] != want[0] {
			t.Errorf("arguments = %v, want %v", got.GetArguments(), want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server to observe request")
	}
}

func TestCall_ReplyFail(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)
		readFrame(t, r)
		writeFailFrame(t, conn, pb.CoreErrorNotification_CR_NOT_FOUND)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()

	err = c.Call(RunCommandID, &pb.CoreRunCommandRequest{Command: new("x")}, &pb.EmptyMessage{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	rpcErr, ok := errors.AsType[*RPCError](err)
	if !ok {
		t.Fatalf("expected *RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != pb.CoreErrorNotification_CR_NOT_FOUND {
		t.Errorf("code = %v, want CR_NOT_FOUND", rpcErr.Code)
	}
}

func TestCall_TextNotificationThenResult(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)
		readFrame(t, r)

		note := &pb.CoreTextNotification{
			Fragments: []*pb.CoreTextFragment{
				{Text: new("shig\xa2s")},
			},
		}
		body, _ := proto.Marshal(note)
		writeFrame(t, conn, replyText, body)

		out, _ := proto.Marshal(&pb.EmptyMessage{})
		writeFrame(t, conn, replyResult, out)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()

	var received []string
	c.OnText = func(n *pb.CoreTextNotification) {
		for _, f := range n.GetFragments() {
			received = append(received, f.GetText())
		}
	}

	out := &pb.EmptyMessage{}
	if err := c.Call(RunCommandID, &pb.CoreRunCommandRequest{Command: new("x")}, out); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if want := []string{"shigós"}; len(received) != 1 || received[0] != want[0] {
		t.Errorf("received = %q, want %q", received, want)
	}
}

func TestCall_UnexpectedReplyID(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)
		readFrame(t, r)
		writeFrame(t, conn, 42, nil)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()

	err = c.Call(RunCommandID, &pb.CoreRunCommandRequest{Command: new("foo")}, &pb.EmptyMessage{})
	if err == nil || !strings.Contains(err.Error(), "unexpected reply id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCall_ReplySizeOutOfRange(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)
		readFrame(t, r)
		writeFrameRaw(t, conn, replyResult, 0xFFFFFFFF, nil)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()

	err = c.Call(RunCommandID, &pb.CoreRunCommandRequest{Command: new("foo")}, &pb.EmptyMessage{})
	if err == nil || !strings.Contains(err.Error(), "reply size out of range") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBind_Success(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)

		id, body := readFrame(t, r)
		if id != bindMethodID {
			t.Errorf("call id = %d, want bindMethodID (%d)", id, bindMethodID)
		}
		req := &pb.CoreBindRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			t.Errorf("server: unmarshal bind request: %v", err)
		}
		if req.GetMethod() != "SomeMethod" {
			t.Errorf("method = %q, want SomeMethod", req.GetMethod())
		}
		if req.GetPlugin() != "someplugin" {
			t.Errorf("plugin = %q, want someplugin", req.GetPlugin())
		}
		wantIn := string((&pb.EmptyMessage{}).ProtoReflect().Descriptor().FullName())
		if req.GetInputMsg() != wantIn {
			t.Errorf("input_msg = %q, want %q", req.GetInputMsg(), wantIn)
		}

		reply := &pb.CoreBindReply{AssignedId: new(int32(7))}
		out, _ := proto.Marshal(reply)
		writeFrame(t, conn, replyResult, out)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()

	id, err := c.Bind("SomeMethod", "someplugin", &pb.EmptyMessage{}, &pb.EmptyMessage{})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
}

func TestBind_Error(t *testing.T) {
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)
		readFrame(t, r)
		writeFailFrame(t, conn, pb.CoreErrorNotification_CR_NOT_IMPLEMENTED)
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.conn.Close()

	_, err = c.Bind("SomeMethod", "", &pb.EmptyMessage{}, &pb.EmptyMessage{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok := errors.AsType[*RPCError](err); !ok {
		t.Fatalf("expected *RPCError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), `bind "SomeMethod"`) {
		t.Errorf("error = %v, missing bind context", err)
	}
}

func TestClose_SendsQuitFrame(t *testing.T) {
	quitCh := make(chan int16, 1)
	addr := startFakeServer(t, func(conn net.Conn) {
		serverHandshake(t, conn)
		r := bufio.NewReader(conn)
		id, _ := readFrame(t, r)
		quitCh <- id
	})

	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case id := <-quitCh:
		if id != requestQuit {
			t.Errorf("id = %d, want requestQuit (%d)", id, requestQuit)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for quit frame")
	}
}

func TestDecodeCP437(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"passthrough", "urist", "urist"},
		{"e acute", "\x82", "é"},
		{"o acute", "\xa2", "ó"},
		{"mixed", "shig\xa2s", "shigós"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeCP437(tt.in); got != tt.want {
				t.Errorf("decodeCP437(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
