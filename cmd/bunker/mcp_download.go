package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type (
	mcpDownloadIn struct {
		ID    string `json:"id" jsonschema:"the item id, as list or read return it"`
		Index int    `json:"index,omitempty" jsonschema:"which attachment, from 0 (default 0)"`
		Path  string `json:"path" jsonschema:"absolute path of the file to write (no ~, no relative paths); its directory must exist"`
		Force bool   `json:"force,omitempty" jsonschema:"overwrite an existing file at path (default false: refuse)"`
	}
	mcpDownloadOut struct {
		Path string `json:"path" jsonschema:"where the file was written"`
		Name string `json:"name" jsonschema:"the attachment's name, as the sender gave it"`
		MIME string `json:"mime"`
		Size int64  `json:"size" jsonschema:"bytes written"`
	}
)

// addMCPDownloadTool registers download, the MCP twin of bunker
// download: it saves one attachment's bytes to a local file. Nothing
// leaves the machine, so it is not gated by --allow-send; but it writes
// to disk, so it is not read-only either. It is not destructive by
// default (an existing file is refused unless force), and open-world
// because the daemon may fetch the bytes from the channel's server.
func addMCPDownloadTool(server *mcp.Server, dial mcpDialer) {
	notDestructive, openWorld := false, true
	mcp.AddTool(server, &mcp.Tool{
		Name: "download",
		Description: "Save one attachment of an item (id, index from 0) to a local file at path, an absolute path whose directory exists, like bunker download. " +
			"Refuses to overwrite an existing file unless force is true. Returns path, name, mime and size. " +
			"Writes only on this machine (nothing is sent, so --allow-send does not apply) and never marks anything read. " +
			"To read an attachment's text instead, use attachment.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpDownloadIn) (*mcp.CallToolResult, mcpDownloadOut, error) {
		if in.ID == "" {
			return nil, mcpDownloadOut{}, errors.New("download: id is required")
		}
		// The CLI resolves a relative -o against the user's shell; the
		// MCP server's working directory is whatever the host launched it
		// in, so only an absolute path says where the file lands.
		if !filepath.IsAbs(in.Path) {
			return nil, mcpDownloadOut{}, fmt.Errorf("download: path %q must be an absolute path (no ~ and no relative paths)", in.Path)
		}
		res, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) (mcpDownloadOut, error) {
			res, err := saveAttachment(ctx, b, in.ID, in.Index, in.Path, in.Force)
			return mcpDownloadOut{Path: res.Path, Name: res.Name, MIME: res.MIME, Size: res.Bytes}, err
		})
		if err != nil {
			return nil, mcpDownloadOut{}, err
		}
		return nil, res, nil
	})
}
