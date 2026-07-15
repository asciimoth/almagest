package almagest

type TypePalette struct {
	// TypeColor is a color for rendering links/ports/node primary color in UI
	TypeColor string `json:"type"`
	// TypeColor is a color for rendering text over TypeColor background in UI
	TextColor string `json:"text"`
}

// Color palette for builtin link/port tyles
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
	TypeNetwork: {
		TypeColor: "#8a5cf6",
		TextColor: "#ffffff",
	},
	TypeTun: {
		TypeColor: "#c47a3c",
		TextColor: "#ffffff",
	},
}
