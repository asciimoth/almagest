package lib

type TypePalette struct {
	// TypeColor is a color for rendering links/ports/node primary color in UI
	TypeColor string `json:"type"`
	// TypeColor is a color for rendering text over TypeColor background in UI
	TextColor string `json:"text"`
}

// Color palette for builtin link/port tyles
var DefaultColorMap = map[string]TypePalette{
	"almagest/network": {
		TypeColor: "#8a5cf6",
		TextColor: "#ffffff",
	},
}
