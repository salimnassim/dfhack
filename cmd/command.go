// Command cmd runs a single DFHack console command over RPC and prints its
// output.
//
// Usage:
//
//	cmd [-addr host:port] [-command name] [args...]
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/salimnassim/dfhack/client"
	pb "github.com/salimnassim/dfhack/gen/proto"
)

func main() {
	addr := flag.String("addr", client.DefaultAddr, "remote server address")
	command := flag.String("command", "ls", "console command to run")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *addr, *command, flag.Args()); err != nil {
		slog.Error("failed to run dfhack command", "error", err)
		stop()
		os.Exit(1)
	}
}

func run(ctx context.Context, addr, command string, args []string) error {
	c, err := client.Dial(ctx, addr)
	if err != nil {
		return err
	}
	defer c.Close()

	c.OnText = func(n *pb.CoreTextNotification) {
		for _, fragment := range n.GetFragments() {
			fmt.Print(fragment.GetText())
		}
	}

	req := &pb.CoreRunCommandRequest{Command: new(command), Arguments: args}
	return c.Call(ctx, client.RunCommandID, req, &pb.EmptyMessage{})
}
