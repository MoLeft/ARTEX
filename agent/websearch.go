package agent

import (
	"strings"

	"github.com/Autumn-27/artex/websearch"
	"github.com/Autumn-27/norma/agentcore"
)

// configureWebSearch keeps Norma's existing providers unchanged. AnySearch is
// provided by the host until Norma includes it; turn off the SDK's automatic
// search registration so only one tool named web_search reaches the session.
func configureWebSearch(opts *agentcore.Options, search WebSearchOpts) error {
	if !search.Enabled || !strings.EqualFold(strings.TrimSpace(search.Backend), "anysearch") {
		return nil
	}
	searchTool, err := websearch.NewAnySearch(search.AnySearchKey, search.Proxy)
	if err != nil {
		return err
	}
	opts.EnableWebSearch = false
	opts.Tools = append(opts.Tools, searchTool)
	return nil
}
