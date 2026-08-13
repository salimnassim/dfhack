package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	pb "github.com/salimnassim/dfhack/gen/proto"
	"golang.org/x/text/encoding/charmap"
	"google.golang.org/protobuf/proto"
)

const DefaultAddr = "127.0.0.1:5000"

const (
	requestMagic    = "DFHack?\n"
	responseMagic   = "DFHack!\n"
	protocolVersion = 1
)

const headerSize = 8
const maxMessageSize = 64 * 1048576

const (
	replyResult int16 = -1 // body is the output message
	replyFail   int16 = -2 // no body; header size carries the error code
	replyText   int16 = -3 // body is a CoreTextNotification
	requestQuit int16 = -4 // client -> server: close the connection
)

const (
	bindMethodID int16 = 0
	RunCommandID int16 = 1
)

// Base client which is NOT thread-safe.
type Client struct {
	conn net.Conn
	r    *bufio.Reader

	// Invokes on text notifications the server emits
	OnText func(*pb.CoreTextNotification)
}

// Dial opens a TCP connection to addr and performs the DFHack handshake.
func Dial(ctx context.Context, addr string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	c := &Client{conn: conn, r: bufio.NewReader(conn)}
	if err := c.handshake(); err != nil {
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
	if string(resp[:len(responseMagic)]) != responseMagic {
		return fmt.Errorf("bad handshake magic %q", resp[:len(responseMagic)])
	}
	if v := binary.LittleEndian.Uint32(resp[len(responseMagic):]); v != protocolVersion {
		return fmt.Errorf("unsupported protocol version %d", v)
	}
	return nil
}

func (c *Client) Close() error {
	var hdr [headerSize]byte
	id := requestQuit
	binary.LittleEndian.PutUint16(hdr[0:2], uint16(id))
	_, _ = c.conn.Write(hdr[:])
	return c.conn.Close()
}

// Bind resolves a plugin-provided RPC method name to a numeric ID for use with Call.
func (c *Client) Bind(method, plugin string, in, out proto.Message) (int16, error) {
	req := &pb.CoreBindRequest{
		Method:    new(method),
		InputMsg:  new(string(in.ProtoReflect().Descriptor().FullName())),
		OutputMsg: new(string(out.ProtoReflect().Descriptor().FullName())),
	}
	if plugin != "" {
		req.Plugin = new(plugin)
	}

	reply := &pb.CoreBindReply{}
	if err := c.Call(bindMethodID, req, reply); err != nil {
		return 0, fmt.Errorf("bind %q: %w", method, err)
	}
	return int16(reply.GetAssignedId()), nil
}

// Call sends in as the request body for method id and decodes the reply into out.
func (c *Client) Call(id int16, in, out proto.Message) error {
	if err := c.writeMessage(id, in); err != nil {
		return err
	}
	return c.readReply(out)
}

func (c *Client) writeMessage(id int16, msg proto.Message) error {
	body, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if len(body) > maxMessageSize {
		return fmt.Errorf("request too large (%d bytes)", len(body))
	}

	buf := make([]byte, headerSize+len(body))
	binary.LittleEndian.PutUint16(buf[0:2], uint16(id))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(body)))
	copy(buf[headerSize:], body)

	if _, err := c.conn.Write(buf); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	return nil
}

func (c *Client) readReply(out proto.Message) error {
	for {
		var hdr [headerSize]byte
		if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
			return fmt.Errorf("read reply header: %w", err)
		}
		id := int16(binary.LittleEndian.Uint16(hdr[0:2]))
		size := int32(binary.LittleEndian.Uint32(hdr[4:8]))

		switch id {
		case replyFail:
			return &RPCError{Code: pb.CoreErrorNotification_ErrorCode(size)}
		case replyResult, replyText:
			if size < 0 || size > maxMessageSize {
				return fmt.Errorf("reply size out of range (%d)", size)
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(c.r, body); err != nil {
				return fmt.Errorf("read reply body: %w", err)
			}
			if id == replyText {
				note := &pb.CoreTextNotification{}
				if err := proto.Unmarshal(body, note); err != nil {
					return fmt.Errorf("unmarshal text notification: %w", err)
				}
				for _, frag := range note.Fragments {
					if frag.Text != nil {
						decoded := decodeCP437(*frag.Text)
						frag.Text = &decoded
					}
				}
				if c.OnText != nil {
					c.OnText(note)
				}
				continue
			}
			if err := proto.Unmarshal(body, out); err != nil {
				return fmt.Errorf("unmarshal result: %w", err)
			}
			return nil
		default:
			return fmt.Errorf("unexpected reply id %d", id)
		}
	}
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
