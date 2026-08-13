# dfhack

Go client for [DFHack](https://github.com/DFHack/dfhack) RPC server. Communication happens with DFHack's binary TCP protocol (handshake, fixed frames, bind/call/quit)

## Contents

- `client/` the RPC client (`Dial`, `Bind`, `Call`, `Close`). Text delivered via
  `OnText` is automatically converted from DFHack's CP437 codepage to UTF-8, so
  non-ASCII names and glyphs render correctly.
- `gen/proto/` generated Go bindings for DFHack's core protobuf messages
  (`CoreProtocol.proto`, `Basic.proto`, `BasicApi.proto`), regenerated from upstream
  DFHack `.proto` files via `make proto` (see `scripts/proto-entrypoint.sh`).
- `cmd/` a minimal CLI example that runs a single DFHack console command.

## Install

```sh
go get github.com/salimnassim/dfhack
```

## Usage

Connect and run a DFHack console command
(`CoreRunCommandRequest`/`RunCommandID` are always available, no `Bind` needed):

```go
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/salimnassim/dfhack/client"
	pb "github.com/salimnassim/dfhack/gen/proto"
	"google.golang.org/protobuf/proto"
)

func main() {
	ctx := context.Background()

	c, err := client.Dial(ctx, client.DefaultAddr)
	if err != nil {
		slog.Error("dial failed", "error", err)
		return
	}
	defer c.Close()

	// Streamed text output (e.g. command output) arrives via OnText.
	c.OnText = func(n *pb.CoreTextNotification) {
		for _, fragment := range n.GetFragments() {
			fmt.Println(fragment.GetText())
		}
	}

	cmd := &pb.CoreRunCommandRequest{
		Command:   proto.String("ls"),
		Arguments: nil,
	}
	if err := c.Call(client.RunCommandID, cmd, &pb.EmptyMessage{}); err != nil {
		slog.Error("command failed", "error", err)
	}
}
```

A runnable version of this is in [`cmd/command.go`](cmd/command.go):

```sh
go run ./cmd -addr 127.0.0.1:5000 -command ls
```

### Text encoding

Dwarf Fortress encodes in-game text (item names, creature names, etc.) using
the DOS CP437 codepage rather than UTF-8. The client transparently decodes
`CoreTextFragment` text from CP437 to UTF-8 before it reaches `OnText`, so
accented and special characters (e.g. `é`) print correctly instead of showing
up as `�` or `?`.

### Calling a plugin RPC method

Plugin-provided methods must be bound to an ID before use with `Client.Bind`, then invoked with `Client.Call` using that ID:

```go
id, err := c.Bind("SomeMethod", "someplugin", &pb.SomeMethodIn{}, &pb.SomeMethodOut{})
if err != nil {
	// handle error
}

out := &pb.SomeMethodOut{}
if err := c.Call(id, &pb.SomeMethodIn{ /* ... */ }, out); err != nil {
	// handle error
}
```

## Regenerating protobuf bindings

Generated code under `gen/proto/` is produced from upstream DFHack `.proto` files:

```sh
make proto DFHACK_VERSION=<tag>
```

See `Makefile`, `buf.gen.yaml`, and `scripts/proto-entrypoint.sh` for details.
