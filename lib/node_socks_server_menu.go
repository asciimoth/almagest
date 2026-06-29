package almagest

import (
	"github.com/asciimoth/formular"
	"github.com/asciimoth/socksgo/protocol"
)

func (n *socksServerNode) updateLabel() error {
	return n.SetLabel(socksServerConfigLabel(n.config))
}

func (n *socksServerNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock(), n.logsBlock())
}

func (n *socksServerNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, true, n.configBlock(), n.logsBlock())
}

func (n *socksServerNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.configBlock())
}

func (n *socksServerNode) sendLogsBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.logsBlock())
}

func (n *socksServerNode) configBlock() formular.Block {
	return socksServerConfigBlock(n.config, n.formError)
}

func (n *socksServerNode) logsBlock() formular.Block {
	return logsBlock("logs", "Worker", 20, n.logs)
}

func (n *socksServerNode) appendLog(level, text string) {
	appendCappedLog(&n.logs, 60, level, text)
}

func socksServerTextField(id, label, value string, statusErr bool, statusText string) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.TextField(id, label, value, opts...)
}

func socksServerIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	opts := []formular.FieldOption{formular.Validation}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.IntField(id, label, value, opts...)
}

func socksServerCheckbox(id, label string, value bool) formular.Item {
	return formular.CheckboxField(id, label, value)
}

func socksServerUsersField(users []socksServerUser) formular.Item {
	template := socksServerUserTemplate()
	return formular.Item{Type: formular.ItemField, ID: "users", Label: "Users", Field: &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{template},
		Elements:  socksServerUserElements(users),
	}}
}

func socksServerUserTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "user",
		Label: "User",
		Items: []formular.Item{
			formular.TextField("username", "Username", ""),
			formular.SecretTextField("password", "Password", ""),
		},
	}
}

func socksServerUserElements(users []socksServerUser) []formular.ArrayElement {
	template := socksServerUserTemplate()
	out := make([]formular.ArrayElement, 0, len(users))
	for _, user := range users {
		out = append(out, formular.InstantiateArrayTemplate(template, user.id, map[string]any{"username": user.username, "password": user.password}))
	}
	return out
}

func socksServerConfigBlock(config socksServerConfig, formError string) formular.Block {
	statusErr := formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		socksServerTextField("socksHost", "SOCKS host", config.socksHost, statusErr, formError),
		socksServerIntField("socksPort", "SOCKS port", config.socksPort, statusErr, formError),
		socksServerTextField("wsHost", "SOCKS+WS host", config.wsHost, statusErr, formError),
		socksServerIntField("wsPort", "SOCKS+WS port", config.wsPort, statusErr, formError),
		socksServerTextField("wsPath", "SOCKS+WS path", config.wsPath, false, ""),
		socksServerUsersField(config.users),
		socksServerCheckbox("cmdConnect", "CONNECT", config.commands[protocol.CmdConnect]),
		socksServerCheckbox("cmdBind", "BIND", config.commands[protocol.CmdBind]),
		socksServerCheckbox("cmdUDPAssoc", "UDP ASSOC", config.commands[protocol.CmdUDPAssoc]),
		socksServerCheckbox("cmdTorResolve", "Tor RESOLVE", config.commands[protocol.CmdTorResolve]),
		socksServerCheckbox("cmdTorResolvePtr", "Tor RESOLVE_PTR", config.commands[protocol.CmdTorResolvePtr]),
		socksServerCheckbox("cmdGostMuxBind", "Gost MBIND", config.commands[protocol.CmdGostMuxBind]),
		socksServerCheckbox("cmdGostUDPTun", "Gost UDPTUN", config.commands[protocol.CmdGostUDPTun]),
	}}
}
