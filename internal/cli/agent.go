package cli

import (
	"context"
	"fmt"
	"io"

	outputpkg "github.com/Dematom1/berth/internal/output"
	remotepkg "github.com/Dematom1/berth/internal/remote"
)

func (app application) runAgent(ctx context.Context, args []string, input io.Reader, out io.Writer) int {
	if len(args) == 1 && args[0] == "--help" {
		fmt.Fprint(out, help("agent"))
		return 0
	}
	if len(args) != 0 {
		return outputpkg.Error(out, outputpkg.Auto, 2, "agent takes no flags; use berth agent --help", help("agent"))
	}
	return remotepkg.Serve(ctx, app.version, input, out, func(ctx context.Context, args []string, in io.Reader, out io.Writer, mode outputpkg.Mode) int {
		if args[0] == "job" {
			return app.runJob(ctx, args[1:], out, outputpkg.Auto, in, "session")
		}
		return app.runWithOutput(ctx, args, out, mode)
	})
}
