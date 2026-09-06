package sockstest

import (
	"github.com/asciimoth/formular"
	"github.com/asciimoth/socksgo/protocol"

	socksnodes "github.com/asciimoth/almagest/lib/socks"
)

// User is a SOCKS server user used by e2e test form helpers.
type User struct {
	ID             string
	Username       string
	Password       string
	PasswordSecret string
}

// ServerUserValues builds form values for SOCKS server users.
func ServerUserValues(users []User) []formular.ArrayElementValue {
	out := make([]formular.ArrayElementValue, 0, len(users))
	for _, user := range users {
		out = append(out, formular.ArrayElementValue{
			ID:       user.ID,
			Template: "user",
			Values: map[string]any{
				"username":       user.Username,
				"password":       user.Password,
				"passwordSecret": user.PasswordSecret,
			},
		})
	}
	return out
}

// DefaultServerCommands returns default SOCKS server command settings.
func DefaultServerCommands() map[protocol.Cmd]bool {
	return socksnodes.DefaultServerCommands()
}

// ServerCommandFields returns form field IDs for SOCKS server commands.
func ServerCommandFields() map[protocol.Cmd]string {
	return socksnodes.ServerCommandFields()
}
