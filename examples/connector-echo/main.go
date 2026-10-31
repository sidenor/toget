// Command connector-echo is a minimal Toget connector built with the v1 Go
// SDK. It streams the mention text and posts a formatted echo reply.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/sidenor/toget/sdk/connector"
)

func main() {
	connector.Main(func(ctx context.Context, req connector.Request, stream *connector.Emitter) (connector.Result, error) {
		if err := ctx.Err(); err != nil {
			return connector.Result{}, err
		}
		text := strings.TrimSpace(req.Trigger.Text)
		if err := stream.Delta("Echo: "); err != nil {
			return connector.Result{}, err
		}
		if err := stream.Delta(text); err != nil {
			return connector.Result{}, err
		}
		return connector.Result{
			Text:   fmt.Sprintf("**%s** heard: %s", req.Agent.Name, text),
			Format: "markdown",
		}, nil
	})
}
