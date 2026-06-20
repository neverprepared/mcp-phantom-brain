// pbrainctl is the single binary for phantom-brain v5: MCP server, daemon, and
// operator CLI in one. The subcommand picks the mode.
//
// Phase 0 wires the cobra skeleton, a real `version` subcommand, and a
// minimal `mcp` subcommand that registers a single round-trip ping tool —
// just enough to validate the mark3labs/mcp-go stdio integration.
//
// `serve` and all operator subcommands are stubs for now; they print a
// "not implemented in Phase 0" message and exit non-zero. They will be
// filled in across Phases 2-3 per the v5.0 spec.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/mindmorass/mcp-phantom-brain/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:   "pbrainctl",
		Short: "phantom-brain — MCP server, daemon, and operator CLI",
		Long: `pbrainctl is a single binary serving three modes:

  pbrainctl mcp          stdio JSON-RPC MCP server (per agent process)
  pbrainctl serve        HTTP daemon (per-(profile, vault) reaper + synthesizer)
  pbrainctl <op>         operator commands (list, snapshot, vault, ...)

See https://github.com/mindmorass/mcp-phantom-brain for the v5 spec.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(versionCmd())
	root.AddCommand(mcpCmd())
	root.AddCommand(serveCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "pbrainctl: %v\n", err)
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build metadata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(),
				"pbrainctl %s\n  commit: %s\n  built:  %s\n",
				version.Version, version.Commit, version.BuildDate,
			)
			return nil
		},
	}
}

// mcpCmd runs the stdio JSON-RPC MCP server. Phase-0 scope: a single
// `pbrain_ping` tool that round-trips a timestamp so we can verify the
// mark3labs/mcp-go integration end-to-end from Claude Code before
// porting any of the real tools.
func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run as a stdio JSON-RPC MCP server",
		Long: `Starts an MCP server on stdio. Intended to be spawned by Claude Code
via .claude.json mcpServers entries. Phase 0 only exposes a single ping
tool; real tools land in later phases.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv := server.NewMCPServer(
				"phantom-brain",
				version.Version,
				server.WithToolCapabilities(false),
			)

			pingTool := mcp.NewTool("pbrain_ping",
				mcp.WithDescription("Round-trip health probe. Returns a timestamp and the input echo."),
				mcp.WithString("echo",
					mcp.Description("Optional string to echo back. Useful for stdio framing tests."),
				),
			)

			srv.AddTool(pingTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				echo, _ := req.RequireString("echo")
				now := time.Now().UTC().Format(time.RFC3339Nano)
				body := fmt.Sprintf("pong @ %s", now)
				if echo != "" {
					body = fmt.Sprintf("%s (echo: %q)", body, echo)
				}
				return mcp.NewToolResultText(body), nil
			})

			return server.ServeStdio(srv)
		},
	}
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run as the HTTP daemon (not implemented in Phase 0)",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("serve: not implemented in Phase 0 (see v5 spec, Phase 2)")
		},
	}
}
