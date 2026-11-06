package agent

import (
	"strings"

	"github.com/Autumn-27/artex/websearch"
	"github.com/Autumn-27/norma/agentcore"
	actool "github.com/Autumn-27/norma/tool"
)

// configureWebSearch preserves Norma's built-in providers while registering
// host-owned backends exactly once under the same web_search tool name.
func configureWebSearch(opts *agentcore.Options, search WebSearchOpts) error {
	if !search.Enabled {
		return nil
	}
	var searchTool actool.CoreTool
	var err error
	switch strings.ToLower(strings.TrimSpace(search.Backend)) {
	case "anysearch":
		searchTool, err = websearch.NewAnySearch(search.AnySearchKey, search.Proxy)
	case "uapi":
		searchTool, err = websearch.NewUAPI(search.UAPIKey, search.Proxy)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	opts.EnableWebSearch = false
	opts.Tools = append(opts.Tools, searchTool)
	return nil
}
