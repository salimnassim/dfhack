package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"

	"github.com/salimnassim/dfhack/client"
	pb "github.com/salimnassim/dfhack/gen/proto"
)

func main() {
	address := flag.String("addr", client.DefaultAddr, "remote server address")
	command := flag.String("command", "ls", "console command to run")
	args := flag.Args()

	flag.Parse()

	err := run(context.Background(), *address, command, args)
	if err != nil {
		slog.Error("failed to run dfhack command", "error", err)
	}
}

func run(ctx context.Context, addr string, command *string, args []string) error {
	c, err := client.Dial(ctx, addr)
	if err != nil {
		return err
	}
	defer c.Close()

	c.OnText = func(n *pb.CoreTextNotification) {
		for _, fragment := range n.GetFragments() {
			slog.Info(fmt.Sprintf("%s", fragment.GetText()))
		}
	}

	cmd := &pb.CoreRunCommandRequest{Command: command, Arguments: args}
	if err := c.Call(client.RunCommandID, cmd, &pb.EmptyMessage{}); err != nil {
		return err
	}

	return nil
}
