// Package client implements a client for DFHack's RPC server, speaking its
// binary TCP protocol (handshake, fixed-size frames, bind/call/quit) directly.
package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/text/encoding/charmap"

	pb "github.com/salimnassim/dfhack/gen/proto"
	"google.golang.org/protobuf/proto"
)

// DefaultAddr is the address DFHack's RPC server listens on by default.
const DefaultAddr = "127.0.0.1:5000"

const (
	requestMagic    = "DFHack?\n"
	responseMagic   = "DFHack!\n"
	protocolVersion = 1
)

const (
	headerSize     = 8
	maxMessageSize = 64 << 20
)

const (
	replyResult int16 = -1 // body is the output message
	replyFail   int16 = -2 // no body; header size carries the error code
	replyText   int16 = -3 // body is a CoreTextNotification
	requestQuit int16 = -4 // client -> server: close the connection
)

const (
	bindMethodID int16 = 0

	// RunCommandID is the fixed method ID of DFHack's core RunCommand RPC,
	// which takes a CoreRunCommandRequest and returns an EmptyMessage. It
	// needs no Bind.
	RunCommandID int16 = 1
)

var (
	// ErrBadMagic is returned by Dial when the server does not answer the
	// handshake with DFHack's response magic.
	ErrBadMagic = errors.New("bad handshake magic")
	// ErrProtocolVersion is returned by Dial when the server speaks an
	// unsupported protocol version.
	ErrProtocolVersion = errors.New("unsupported protocol version")
	// ErrUnexpectedReply is returned when the server replies with an unknown
	// frame ID.
	ErrUnexpectedReply = errors.New("unexpected reply id")
	// ErrFrameTooLarge is returned when a request or reply body exceeds the
	// protocol's maximum message size.
	ErrFrameTooLarge = errors.New("frame too large")
)

// Client is a connection to a DFHack RPC server.
//
// A Client is not safe for concurrent use: it carries one in-flight request
// at a time. After an I/O or framing error the connection's stream position
// is unknown, so every later Call fails with that error; Dial a new Client to
// recover.
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	broken error

	// OnText, if set, is called synchronously from within Call for each text
	// notification (e.g. console output) the server sends before the reply.
	// Fragment text has already been decoded from CP437 to UTF-8.
	OnText func(*pb.CoreTextNotification)
}

// Dial opens a TCP connection to addr and performs the DFHack handshake.
// ctx bounds both the connect and the handshake.
func Dial(ctx context.Context, addr string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	c := &Client{conn: conn, r: bufio.NewReader(conn)}
	if err := c.withContext(ctx, c.handshake); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) handshake() error {
	req := make([]byte, len(requestMagic)+4)
	copy(req, requestMagic)
	binary.LittleEndian.PutUint32(req[len(requestMagic):], protocolVersion)
	if _, err := c.conn.Write(req); err != nil {
		return fmt.Errorf("send handshake: %w", err)
	}

	resp := make([]byte, len(responseMagic)+4)
	if _, err := io.ReadFull(c.r, resp); err != nil {
		return fmt.Errorf("read handshake: %w", err)
	}
	if magic := resp[:len(responseMagic)]; string(magic) != responseMagic {
		return fmt.Errorf("%w: %q", ErrBadMagic, magic)
	}
	if v := binary.LittleEndian.Uint32(resp[len(responseMagic):]); v != protocolVersion {
		return fmt.Errorf("%w: %d", ErrProtocolVersion, v)
	}
	return nil
}

// withContext runs fn, which performs I/O on c.conn, aborting that I/O when
// ctx is done. If ctx ends while fn runs, the returned error is ctx's.
func (c *Client) withContext(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	// A deadline in the past unblocks any pending Read or Write.
	stop := context.AfterFunc(ctx, func() { c.conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	err := fn()
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return err
}

// Close tells the server the client is quitting and closes the connection.
func (c *Client) Close() error {
	var hdr [headerSize]byte
	putHeader(hdr[:], requestQuit, 0)
	// The quit notice is a courtesy; the server also handles a plain
	// disconnect, so a failed write here is not worth reporting.
	_, _ = c.conn.Write(hdr[:])
	return c.conn.Close()
}

// Bind resolves a plugin-provided RPC method name to a numeric ID for use with
// Call. in and out are only used for their message types. An empty plugin
// binds a core method.
func (c *Client) Bind(ctx context.Context, method, plugin string, in, out proto.Message) (int16, error) {
	req := &pb.CoreBindRequest{
		Method:    new(method),
		InputMsg:  new(string(in.ProtoReflect().Descriptor().FullName())),
		OutputMsg: new(string(out.ProtoReflect().Descriptor().FullName())),
	}
	if plugin != "" {
		req.Plugin = new(plugin)
	}

	reply := &pb.CoreBindReply{}
	if err := c.Call(ctx, bindMethodID, req, reply); err != nil {
		return 0, fmt.Errorf("bind %q: %w", method, err)
	}
	return int16(reply.GetAssignedId()), nil
}

// Call sends in as the request body for method id and decodes the reply into
// out. A server-side failure is returned as an *RPCError.
func (c *Client) Call(ctx context.Context, id int16, in, out proto.Message) error {
	if c.broken != nil {
		return fmt.Errorf("connection unusable after earlier error: %w", c.broken)
	}
	return c.withContext(ctx, func() error {
		if err := c.writeMessage(id, in); err != nil {
			return err
		}
		return c.readReply(out)
	})
}

// fail marks the connection as out of sync and returns err.
func (c *Client) fail(err error) error {
	c.broken = err
	return err
}

func putHeader(b []byte, id int16, size uint32) {
	binary.LittleEndian.PutUint16(b[0:2], uint16(id))
	binary.LittleEndian.PutUint32(b[4:8], size)
}

func (c *Client) writeMessage(id int16, msg proto.Message) error {
	body, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if len(body) > maxMessageSize {
		return fmt.Errorf("%w: request is %d bytes", ErrFrameTooLarge, len(body))
	}

	buf := make([]byte, headerSize+len(body))
	putHeader(buf, id, uint32(len(body)))
	copy(buf[headerSize:], body)

	if _, err := c.conn.Write(buf); err != nil {
		return c.fail(fmt.Errorf("send request: %w", err))
	}
	return nil
}

func (c *Client) readReply(out proto.Message) error {
	for {
		var hdr [headerSize]byte
		if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
			return c.fail(fmt.Errorf("read reply header: %w", err))
		}
		id := int16(binary.LittleEndian.Uint16(hdr[0:2]))
		size := int32(binary.LittleEndian.Uint32(hdr[4:8]))

		switch id {
		case replyFail:
			return &RPCError{Code: pb.CoreErrorNotification_ErrorCode(size)}
		case replyResult, replyText:
			if size < 0 || size > maxMessageSize {
				return c.fail(fmt.Errorf("%w: reply is %d bytes", ErrFrameTooLarge, size))
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(c.r, body); err != nil {
				return c.fail(fmt.Errorf("read reply body: %w", err))
			}
			if id == replyText {
				if err := c.handleText(body); err != nil {
					return err
				}
				continue
			}
			if err := proto.Unmarshal(body, out); err != nil {
				return fmt.Errorf("unmarshal result: %w", err)
			}
			return nil
		default:
			return c.fail(fmt.Errorf("%w: %d", ErrUnexpectedReply, id))
		}
	}
}

func (c *Client) handleText(body []byte) error {
	note := &pb.CoreTextNotification{}
	if err := proto.Unmarshal(body, note); err != nil {
		return fmt.Errorf("unmarshal text notification: %w", err)
	}
	for _, frag := range note.Fragments {
		if frag.Text != nil {
			frag.Text = new(decodeCP437(*frag.Text))
		}
	}
	if c.OnText != nil {
		c.OnText(note)
	}
	return nil
}

var cp437Decoder = charmap.CodePage437.NewDecoder()

func decodeCP437(s string) string {
	out, err := cp437Decoder.String(s)
	if err != nil {
		return s
	}
	return out
}

// RPCError reports a replyFail response, wrapping the DFHack error code.
type RPCError struct {
	Code pb.CoreErrorNotification_ErrorCode
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc failed with code %s (%d)", e.Code, int32(e.Code))
}
