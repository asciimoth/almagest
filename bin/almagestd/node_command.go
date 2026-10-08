package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	experimentalnodes "github.com/asciimoth/almagest/lib/experimental"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// NodeTypeProtectedCommand is the daemon-only node class for scheduling a
	// protected command-runner allowlist entry from a pasta/trigger event.
	NodeTypeProtectedCommand = "almagest/Command"

	commandNodeConfigBlockID = "config"
	commandNodeCommandField  = "command"
	commandNodeTriggerPort   = "Trigger"
)

var (
	errCommandNodeNoDispatcher = errors.New("command runner is unavailable")
	errCommandNodeNotAllowed   = errors.New("command is not allowed for nodes")
)

// daemonNodeClasses returns classes that almagestd must create explicitly.
// These classes either depend on daemon internals or require protected host
// configuration and opt-in registration.
func daemonNodeClasses(cfg cmdrunner.Config, dispatcher daemonservices.NodeCommandDispatcher, enableExperimental bool, experimental ProtectedExperimentalConfig) []pasta.NodeClass {
	classes := []pasta.NodeClass{
		newCommandNodeClass(cfg, dispatcher),
	}
	if enableExperimental {
		classes = append(classes, experimentalnodes.XTorDriverClass{Host: experimental.TorDriver})
	}
	return classes
}

// daemonExperimentalNodeTypes returns the stability metadata for opt-in node
// classes. Keep this list next to daemonNodeClasses so registration and API
// classification change together.
func daemonExperimentalNodeTypes(enableExperimental bool) map[string]bool {
	types := make(map[string]bool)
	if enableExperimental {
		types[experimentalnodes.NodeTypeXTorDriver] = true
	}
	return types
}

// commandNodeClass exposes a protected command-runner allowlist as a Pasta node
// class. It stores only command metadata needed for menu choices and port
// reconciliation; the actual process execution path remains owned by
// command.Runner.
type commandNodeClass struct {
	catalog    commandNodeCatalog
	dispatcher daemonservices.NodeCommandDispatcher
}

func newCommandNodeClass(cfg cmdrunner.Config, dispatcher daemonservices.NodeCommandDispatcher) commandNodeClass {
	return commandNodeClass{
		catalog:    newCommandNodeCatalog(cfg),
		dispatcher: dispatcher,
	}
}

func (commandNodeClass) ClassName() string        { return NodeTypeProtectedCommand }
func (commandNodeClass) ShortDescription() string { return "Protected command" }
func (commandNodeClass) LongDescription() string {
	return "Schedules protected almagestd command-runner entries that explicitly set AllowNodes in protected config. The node has one left-directed pasta/trigger input and one left-directed pasta/string input for each selected command slot. It has no output ports."
}
func (commandNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: std.TypeTrigger, InitialPorts: []pasta.Port{
		commandNodeTriggerInputPort(),
	}}
}
func (c commandNodeClass) NewNode(cfg configer.Config, previous ...*pasta.NodeClassState) (pasta.Node, error) {
	config := readCommandNodeConfig(cfg)
	if state := firstCommandNodeState(previous); state != nil {
		c.reconcileClassState(config.command, state)
	}
	return &commandNode{
		catalog:    c.catalog,
		dispatcher: c.dispatcher,
		command:    config.command,
		inputs:     map[string]commandNodeInputValue{},
		slotPorts:  map[string]uint64{},
		slotsByID:  map[uint64]string{},
	}, nil
}

func (c commandNodeClass) reconcileClassState(command string, state *pasta.NodeClassState) {
	if state == nil {
		return
	}
	def, ok := c.catalog.command(command)
	left := []pasta.Port{commandNodeTriggerInputPort()}
	if ok {
		for _, slot := range def.slots {
			left = append(left, commandNodeSlotInputPort(slot.name))
		}
	}
	state.PrimaryType = std.TypeTrigger
	state.LeftPorts = commandNodeMergeStatePorts(left, state.LeftPorts)
	state.RightPorts = nil
}

type commandNodeConfig struct {
	command string
}

func readCommandNodeConfig(cfg configer.Config) commandNodeConfig {
	if cfg == nil {
		return commandNodeConfig{}
	}
	return commandNodeConfig{
		command: strings.TrimSpace(configer.String(cfg, configer.Path{commandNodeCommandField}, "")),
	}
}

type commandNodeCatalog struct {
	names    []string
	commands map[string]commandNodeDefinition
}

type commandNodeDefinition struct {
	name  string
	slots []commandNodeSlot
}

type commandNodeSlot struct {
	name string
}

func newCommandNodeCatalog(cfg cmdrunner.Config) commandNodeCatalog {
	names := make([]string, 0, len(cfg.Commands))
	commands := make(map[string]commandNodeDefinition, len(cfg.Commands))
	for name, command := range cfg.Commands {
		if !command.AllowNodes {
			continue
		}
		slots := make([]commandNodeSlot, 0, len(command.Slots))
		for slot := range command.Slots {
			slots = append(slots, commandNodeSlot{name: slot})
		}
		slices.SortFunc(slots, func(a, b commandNodeSlot) int {
			return strings.Compare(a.name, b.name)
		})
		names = append(names, name)
		commands[name] = commandNodeDefinition{name: name, slots: slots}
	}
	slices.Sort(names)
	return commandNodeCatalog{
		names:    names,
		commands: commands,
	}
}

func (c commandNodeCatalog) command(name string) (commandNodeDefinition, bool) {
	def, ok := c.commands[name]
	return def, ok
}

func (c commandNodeCatalog) choices() []formular.Choice {
	choices := make([]formular.Choice, 0, len(c.names))
	for _, name := range c.names {
		choices = append(choices, formular.Choice{Value: name, Label: name})
	}
	return choices
}

// commandNode snapshots string inputs and submits one command-runner request
// when its trigger port receives a trigger event. It owns no process resources;
// command.Runner owns queueing, validation, and child process lifetimes.
type commandNode struct {
	pasta.BasicNode

	catalog    commandNodeCatalog
	dispatcher daemonservices.NodeCommandDispatcher

	w      *pasta.Workspace
	logger pasta.Logger
	id     uint64

	trigger uint64

	command   string
	formError string
	inputs    map[string]commandNodeInputValue
	slotPorts map[string]uint64
	slotsByID map[uint64]string
}

type commandNodeInputValue struct {
	value string
	set   bool
}

func (n *commandNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.logger = logger
	n.id = id
	if n.inputs == nil {
		n.inputs = map[string]commandNodeInputValue{}
	}
	if n.slotPorts == nil {
		n.slotPorts = map[string]uint64{}
	}
	if n.slotsByID == nil {
		n.slotsByID = map[uint64]string{}
	}
	if restored != nil {
		n.trigger = restoredLeft(restored, 0)
	}
	if err := n.w.SetNodePrimaryLocked(n.id, std.TypeTrigger); err != nil {
		return err
	}
	if err := n.rebuildPorts(); err != nil {
		return err
	}
	if err := n.updateLabel(); err != nil {
		return err
	}
	n.sendMenuSnapshot(false)
	return nil
}

func (n *commandNode) OnReady() error {
	n.requestSlotInputs()
	return nil
}

func (n *commandNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if portDirection != "left" {
		return pasta.LinkTypeErr(linkType)
	}
	switch {
	case port == n.trigger:
		if linkType != std.TypeTrigger {
			return pasta.LinkTypeErr(linkType)
		}
		return nil
	case n.slotsByID[port] != "":
		if linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
		snapshot, ok := n.w.PortSnapshotLocked(port)
		if ok && len(snapshot.Links) > 0 {
			return pasta.ErrLinkDup
		}
		return nil
	default:
		return pasta.LinkTypeErr(linkType)
	}
}

func (n *commandNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if portDirection != "left" || linkType != std.TypeString || n.slotsByID[port] == "" {
		return nil
	}
	std.RequestLocked(n.w, n.id, link)
	return nil
}

func (n *commandNode) OnLinkRemoved(_ uint64, port uint64, _ string, portDirection string) error {
	if portDirection != "left" {
		return nil
	}
	slot := n.slotsByID[port]
	if slot == "" {
		return nil
	}
	delete(n.inputs, slot)
	return nil
}

func (n *commandNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "left" {
		return nil
	}
	if port == n.trigger {
		n.trigger = 0
		return nil
	}
	if slot := n.slotsByID[port]; slot != "" {
		delete(n.slotsByID, port)
		delete(n.slotPorts, slot)
		delete(n.inputs, slot)
	}
	return nil
}

func (n *commandNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if receiverPortDirection != "left" {
		return nil
	}
	if event.ReceiverPort == n.trigger && linkType == std.TypeTrigger && !std.IsRequest(event.Payload) {
		n.submitCommand()
		return nil
	}
	slot := n.slotsByID[event.ReceiverPort]
	if slot == "" || linkType != std.TypeString || std.IsRequest(event.Payload) {
		return nil
	}
	value, ok := std.StringFromPayload(event.Payload)
	if !ok {
		return nil
	}
	n.inputs[slot] = commandNodeInputValue{value: value, set: true}
	return nil
}

func (n *commandNode) OnTrigger() error {
	n.submitCommand()
	return nil
}

func (n *commandNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != commandNodeConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != commandNodeConfigBlockID {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *commandNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	if n.command == "" {
		return nil
	}
	return cfg.Set(configer.Path{commandNodeCommandField}, n.command)
}

func (n *commandNode) applyConfigForm(values map[string]any) error {
	next := n.command
	if value, ok := formular.StringValue(values[commandNodeCommandField]); ok {
		next = strings.TrimSpace(value)
	}
	if err := n.validateCommand(next); err != nil {
		n.formError = err.Error()
		n.sendMenuSnapshot(true)
		return nil
	}
	n.formError = ""
	if next == n.command {
		n.sendMenuSnapshot(false)
		return nil
	}
	n.command = next
	n.dropUnknownInputs()
	if err := n.rebuildPorts(); err != nil {
		return err
	}
	if err := n.updateLabel(); err != nil {
		return err
	}
	n.sendMenuSnapshot(true)
	n.requestSlotInputs()
	return nil
}

func (n *commandNode) validateCommand(command string) error {
	if command == "" {
		return nil
	}
	if _, ok := n.catalog.command(command); !ok {
		return fmt.Errorf("%w: %s", errCommandNodeNotAllowed, command)
	}
	return nil
}

func (n *commandNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	if msg.Field.FieldID != commandNodeCommandField {
		sendCommandNodeFieldStatus(n.w, n.id, msg.Field, fmt.Errorf("unknown field %q", msg.Field.FieldID))
		return
	}
	next := n.command
	if value, ok := formular.StringValue(msg.Value); ok {
		next = strings.TrimSpace(value)
	}
	sendCommandNodeFieldStatus(n.w, n.id, msg.Field, n.validateCommand(next))
}

func (n *commandNode) submitCommand() {
	if n.dispatcher == nil {
		n.logWarn("command node trigger dropped: %v", errCommandNodeNoDispatcher)
		return
	}
	def, ok := n.catalog.command(n.command)
	if !ok {
		if n.command != "" {
			n.logWarn("command node trigger dropped: %v: %s", errCommandNodeNotAllowed, n.command)
		}
		return
	}
	req := cmdrunner.Request{
		Name:  def.name,
		Slots: n.snapshotSlots(def),
	}
	if !n.dispatcher.SubmitNodeCommand(req) {
		n.logWarn("command node trigger dropped: command runner is stopped")
	}
}

func (n *commandNode) snapshotSlots(def commandNodeDefinition) map[string]string {
	out := make(map[string]string, len(def.slots))
	for _, slot := range def.slots {
		input := n.inputs[slot.name]
		if input.set {
			out[slot.name] = input.value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (n *commandNode) rebuildPorts() error {
	desired := map[string]commandNodeSlot{}
	if def, ok := n.catalog.command(n.command); ok {
		for _, slot := range def.slots {
			desired[slot.name] = slot
		}
	}
	n.slotPorts = map[string]uint64{}
	n.slotsByID = map[uint64]string{}

	snapshot, ok := n.w.NodeSnapshotLocked(n.id)
	if !ok {
		return pasta.ErrNoNode
	}
	for _, port := range snapshot.LeftPorts {
		portSnapshot, ok := n.w.PortSnapshotLocked(port)
		if !ok {
			continue
		}
		switch {
		case port == n.trigger || (n.trigger == 0 && portSnapshot.Name == commandNodeTriggerPort):
			n.trigger = port
			if err := n.updatePort(port, commandNodeTriggerPort, []string{std.TypeTrigger}); err != nil {
				return err
			}
		case commandNodeSlotFromPortName(portSnapshot.Name, desired) != "":
			slot := commandNodeSlotFromPortName(portSnapshot.Name, desired)
			n.slotPorts[slot] = port
			n.slotsByID[port] = slot
			if err := n.updatePort(port, commandNodeSlotPortName(slot), []string{std.TypeString}); err != nil {
				return err
			}
		default:
			n.w.RemovePortLocked(port)
			delete(n.inputs, commandNodeSlotFromAnyPortName(portSnapshot.Name))
		}
	}
	if n.trigger == 0 {
		port := commandNodeTriggerInputPort()
		port.Node = n.id
		id, err := n.w.AddPortLocked(port)
		if err != nil {
			return err
		}
		n.trigger = id
	}
	for _, slot := range commandNodeSortedSlots(desired) {
		if n.slotPorts[slot.name] != 0 {
			continue
		}
		port := commandNodeSlotInputPort(slot.name)
		port.Node = n.id
		id, err := n.w.AddPortLocked(port)
		if err != nil {
			return err
		}
		n.slotPorts[slot.name] = id
		n.slotsByID[id] = slot.name
	}

	order := []uint64{n.trigger}
	for _, slot := range commandNodeSortedSlots(desired) {
		if port := n.slotPorts[slot.name]; port != 0 {
			order = append(order, port)
		}
	}
	return n.w.SetNodePortOrderLocked(n.id, "left", order)
}

func (n *commandNode) updatePort(port uint64, name string, types []string) error {
	snapshot, ok := n.w.PortSnapshotLocked(port)
	if !ok {
		return pasta.ErrNoPort
	}
	if snapshot.Name != name {
		if err := n.w.SetPortNameLocked(port, name); err != nil {
			return err
		}
	}
	if !slices.Equal(snapshot.Types, types) {
		if err := n.w.SetPortTypesLocked(port, types); err != nil {
			return err
		}
	}
	return nil
}

func (n *commandNode) requestSlotInputs() {
	for _, port := range n.slotPorts {
		if port == 0 {
			continue
		}
		snapshot, ok := n.w.PortSnapshotLocked(port)
		if !ok {
			continue
		}
		for _, link := range snapshot.Links {
			std.RequestLocked(n.w, n.id, link)
		}
	}
}

func (n *commandNode) dropUnknownInputs() {
	def, ok := n.catalog.command(n.command)
	if !ok {
		n.inputs = map[string]commandNodeInputValue{}
		return
	}
	keep := make(map[string]struct{}, len(def.slots))
	for _, slot := range def.slots {
		keep[slot.name] = struct{}{}
	}
	for slot := range n.inputs {
		if _, ok := keep[slot]; !ok {
			delete(n.inputs, slot)
		}
	}
}

func (n *commandNode) updateLabel() error {
	label := "select command"
	if n.command != "" {
		if _, ok := n.catalog.command(n.command); ok {
			label = n.command
		} else {
			label = "unavailable command"
		}
	}
	return n.w.SetNodeLabelLocked(n.id, label)
}

func (n *commandNode) configBlock() formular.Block {
	fieldOptions := []formular.FieldOption{formular.Validation}
	statusErr := n.formError
	if statusErr == "" && n.command != "" {
		if _, ok := n.catalog.command(n.command); !ok {
			statusErr = fmt.Sprintf("%s: %s", errCommandNodeNotAllowed, n.command)
		}
	}
	if statusErr != "" {
		fieldOptions = append(fieldOptions, formular.Status(formular.StatusError), formular.StatusText(statusErr))
	}
	return formular.Block{
		ID: commandNodeConfigBlockID, Order: 10, Generation: 1, Form: true,
		Items: []formular.Item{
			formular.RadioField(commandNodeCommandField, "Command", n.command, n.catalog.choices(), fieldOptions...),
		},
	}
}

func (n *commandNode) sendMenuSnapshot(force bool) {
	if n.w == nil || n.id == 0 {
		return
	}
	menuID := pasta.NodeMenuID(n.id)
	if force {
		n.w.SendNodeMenuMsgLocked(n.id, formular.ForcedMenuSnapshot(menuID, 1, n.configBlock()))
		return
	}
	n.w.SendNodeMenuMsgLocked(n.id, formular.MenuSnapshot(menuID, 1, n.configBlock()))
}

func (n *commandNode) logWarn(format string, args ...any) {
	if n.logger != nil {
		n.logger.Warnf(format, args...)
	}
}

func commandNodeMergeStatePorts(desired, existing []pasta.Port) []pasta.Port {
	out := make([]pasta.Port, 0, len(desired))
	byName := make(map[string]pasta.Port, len(existing))
	for _, port := range existing {
		byName[port.Name] = port
	}
	for _, port := range desired {
		if existing, ok := byName[port.Name]; ok {
			existing.Direction = port.Direction
			existing.Name = port.Name
			existing.Types = port.CopyTypes()
			out = append(out, existing)
			continue
		}
		out = append(out, port)
	}
	return out
}

func commandNodeSortedSlots(slots map[string]commandNodeSlot) []commandNodeSlot {
	out := make([]commandNodeSlot, 0, len(slots))
	for _, slot := range slots {
		out = append(out, slot)
	}
	slices.SortFunc(out, func(a, b commandNodeSlot) int {
		return strings.Compare(a.name, b.name)
	})
	return out
}

func commandNodeSlotFromPortName(portName string, desired map[string]commandNodeSlot) string {
	for name := range desired {
		if portName == commandNodeSlotPortName(name) {
			return name
		}
	}
	return ""
}

func commandNodeSlotFromAnyPortName(portName string) string {
	const prefix = "slot "
	const suffix = " value"
	if !strings.HasPrefix(portName, prefix) || !strings.HasSuffix(portName, suffix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(portName, prefix), suffix)
}

func commandNodeTriggerInputPort() pasta.Port {
	return pasta.Port{Direction: "left", Name: commandNodeTriggerPort, Types: []string{std.TypeTrigger}}
}

func commandNodeSlotInputPort(slot string) pasta.Port {
	return pasta.Port{Direction: "left", Name: commandNodeSlotPortName(slot), Types: []string{std.TypeString}}
}

func commandNodeSlotPortName(slot string) string {
	return "slot " + slot + " value"
}

func firstCommandNodeState(previous []*pasta.NodeClassState) *pasta.NodeClassState {
	if len(previous) == 0 {
		return nil
	}
	return previous[0]
}

func restoredLeft(restored *pasta.NodeInitData, index int) uint64 {
	if restored == nil || index < 0 || index >= len(restored.LeftPorts) {
		return 0
	}
	return restored.LeftPorts[index]
}

func sendCommandNodeFieldStatus(w *pasta.Workspace, node uint64, field formular.FieldRef, err error) {
	if w == nil || node == 0 {
		return
	}
	w.SendNodeMenuMsgLocked(node, formular.FieldStatusFromError(pasta.NodeMenuID(node), 1, 1, field, err))
}
