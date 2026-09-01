package typecolor

import "github.com/asciimoth/almagest/lib/links"

type TypePalette struct {
	// TypeColor is a color for rendering links, ports, and node primary color in UI.
	TypeColor string `json:"type"`
	// TextColor is a color for rendering text over TypeColor background in UI.
	TextColor string `json:"text"`
}

// DefaultColorMap is the color palette for builtin link and port types.
var DefaultColorMap = map[string]TypePalette{
	"pasta/int": {
		TypeColor: "#b37a00",
		TextColor: "#ffffff",
	},
	"pasta/float": {
		TypeColor: "#bd5a20",
		TextColor: "#ffffff",
	},
	"pasta/string": {
		TypeColor: "#087d73",
		TextColor: "#ffffff",
	},
	"pasta/bool": {
		TypeColor: "#3b7f3e",
		TextColor: "#ffffff",
	},
	"pasta/object": {
		TypeColor: "#6f7782",
		TextColor: "#ffffff",
	},
	"pasta/loop": {
		TypeColor: "#2563eb",
		TextColor: "#ffffff",
	},
	"pasta/trigger": {
		TypeColor: "#d6336c",
		TextColor: "#ffffff",
	},
	links.TypeNetwork: {
		TypeColor: "#8a5cf6",
		TextColor: "#ffffff",
	},
	links.TypeDNS: {
		TypeColor: "#1f7a8c",
		TextColor: "#ffffff",
	},
	links.TypeTun: {
		TypeColor: "#c47a3c",
		TextColor: "#ffffff",
	},
	links.TypeWGCtrl: {
		TypeColor: "#5267a8",
		TextColor: "#ffffff",
	},
}
