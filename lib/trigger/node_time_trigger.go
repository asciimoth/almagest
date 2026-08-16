package triggernodes

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeTimeTrigger is the node class name for a wall-clock trigger
	// source.
	NodeTypeTimeTrigger = "almagest/TimeTrigger"

	TimeTriggerModeDisabled = ""
	TimeTriggerModeInterval = "interval"
	TimeTriggerModeDaily    = "daily"
)

const (
	DefaultTimeTriggerInterval  = "2m"
	DefaultTimeTriggerDailyTime = "00:00"
)

const (
	timeTriggerModeDisabled     = TimeTriggerModeDisabled
	timeTriggerModeInterval     = TimeTriggerModeInterval
	timeTriggerModeDaily        = TimeTriggerModeDaily
	defaultTimeTriggerInterval  = DefaultTimeTriggerInterval
	defaultTimeTriggerDailyTime = DefaultTimeTriggerDailyTime
)

var (
	errTimeTriggerMode      = errors.New("mode must be disabled, interval, or daily")
	errTimeTriggerInterval  = errors.New("interval must be a positive duration such as 2m, 30s, or 1h30m")
	errTimeTriggerDailyTime = errors.New("daily time must use HH:MM or HH:MM:SS")
)

type TimeTriggerClass struct {
	Runtime nodekit.Runtime
}

func (TimeTriggerClass) ClassName() string        { return NodeTypeTimeTrigger }
func (TimeTriggerClass) ShortDescription() string { return "Time trigger" }
func (TimeTriggerClass) LongDescription() string {
	return "Emits pasta/trigger events from one right-directed Trigger port on a configured interval or once per day at a local wall-clock time."
}
func (TimeTriggerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: std.TypeTrigger, InitialPorts: []pasta.Port{
		{Direction: "right", Name: "Trigger", Types: []string{std.TypeTrigger}},
	}}
}
func (c TimeTriggerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultTimeTriggerConfig()
	config.read(cfg)
	return &timeTriggerNode{Base: nodekit.Base{Runtime: c.Runtime}, config: config}, nil
}

// timeTriggerConfig is intentionally small and scalar-only so copy/paste and
// workspace save/restore preserve it without custom migration machinery.
type timeTriggerConfig struct {
	mode        string
	interval    string
	dailyTime   string
	emitOnReady bool
}

func defaultTimeTriggerConfig() timeTriggerConfig {
	return timeTriggerConfig{
		mode:      timeTriggerModeDisabled,
		interval:  defaultTimeTriggerInterval,
		dailyTime: defaultTimeTriggerDailyTime,
	}
}

func (c *timeTriggerConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.mode = configer.String(cfg, configer.Path{"mode"}, c.mode)
	c.interval = configer.String(cfg, configer.Path{"interval"}, c.interval)
	c.dailyTime = configer.String(cfg, configer.Path{"dailyTime"}, c.dailyTime)
	c.emitOnReady = configer.Bool(cfg, configer.Path{"emitOnReady"}, c.emitOnReady)
	c.normalize()
}

func (c *timeTriggerConfig) normalize() {
	c.mode = strings.TrimSpace(c.mode)
	c.interval = strings.TrimSpace(c.interval)
	c.dailyTime = strings.TrimSpace(c.dailyTime)
}

func (c timeTriggerConfig) validate() error {
	switch c.mode {
	case timeTriggerModeDisabled:
		return nil
	case timeTriggerModeInterval:
		if _, err := c.intervalDuration(); err != nil {
			return err
		}
		return nil
	case timeTriggerModeDaily:
		if _, err := parseTimeTriggerDailyTime(c.dailyTime); err != nil {
			return err
		}
		return nil
	default:
		return errTimeTriggerMode
	}
}

func (c timeTriggerConfig) runnable() bool {
	return c.validate() == nil && c.mode != timeTriggerModeDisabled
}

func (c timeTriggerConfig) intervalDuration() (time.Duration, error) {
	d, err := time.ParseDuration(c.interval)
	if err != nil || d <= 0 {
		return 0, errTimeTriggerInterval
	}
	return d, nil
}

func (c timeTriggerConfig) label() string {
	switch c.mode {
	case timeTriggerModeInterval:
		return "every " + c.interval
	case timeTriggerModeDaily:
		return "daily " + c.dailyTime
	default:
		return "time trigger"
	}
}

type timeTriggerNode struct {
	nodekit.Base

	out uint64

	config     timeTriggerConfig
	formError  string
	worker     *nodekit.RestartableWorker
	generation uint64
}

type timeTriggerTick struct {
	generation uint64
}

func (n *timeTriggerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.config.normalize()
	n.out = nodekit.RestoredRight(restored, 0)
	if err := n.SetPrimary(std.TypeTrigger); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *timeTriggerNode) OnReady() error {
	if n.config.emitOnReady {
		n.emitAll()
	}
	n.reconcileWorker()
	return nil
}

func (n *timeTriggerNode) OnStop() {
	n.generation++
	n.stopWorker()
}

func (n *timeTriggerNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" || linkType != std.TypeTrigger {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *timeTriggerNode) OnLinkRemoved(_ uint64, _ uint64, _, _ string) error {
	return nil
}

func (n *timeTriggerNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "right" || linkType != std.TypeTrigger || !std.IsRequest(event.Payload) {
		return nil
	}
	return nil
}

func (n *timeTriggerNode) OnTrigger() error {
	n.emitAll()
	return nil
}

func (n *timeTriggerNode) OnInbox(message pasta.InboxMessage) error {
	tick, ok := message.Payload.(timeTriggerTick)
	if !ok || tick.generation != n.generation || !n.config.runnable() {
		return nil
	}
	n.emitAll()
	return nil
}

func (n *timeTriggerNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *timeTriggerNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"mode":        n.config.mode,
		"interval":    n.config.interval,
		"dailyTime":   n.config.dailyTime,
		"emitOnReady": n.config.emitOnReady,
	})
}

func (n *timeTriggerNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := formular.StringValue(values["mode"]); ok {
		next.mode = value
	}
	if value, ok := formular.StringValue(values["interval"]); ok {
		next.interval = value
	}
	if value, ok := formular.StringValue(values["dailyTime"]); ok {
		next.dailyTime = value
	}
	if value, ok := formular.BoolValue(values["emitOnReady"]); ok {
		next.emitOnReady = value
	}
	next.normalize()
	if err := next.validate(); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}

	n.formError = ""
	n.config = next
	n.generation++
	n.stopWorker()
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()
	n.reconcileWorker()
	return nil
}

func (n *timeTriggerNode) reconcileWorker() {
	if !n.config.runnable() {
		n.stopWorker()
		return
	}
	if n.worker != nil {
		return
	}
	generation := n.generation
	config := n.config
	worker, err := nodekit.NewRestartableWorker(n.WorkerSpawner, func(stop <-chan struct{}) {
		runTimeTriggerWorker(n.W, n.ID, generation, config, stop)
	}, "time trigger")
	if err != nil {
		return
	}
	n.worker = worker
}

func (n *timeTriggerNode) stopWorker() {
	if n.worker == nil {
		return
	}
	n.worker.Stop()
	n.worker = nil
}

func runTimeTriggerWorker(w *pasta.Workspace, node, generation uint64, config timeTriggerConfig, stop <-chan struct{}) {
	for {
		delay, ok := timeTriggerNextDelay(time.Now(), config)
		if !ok || !nodekit.SleepOrStopped(stop, delay) {
			return
		}
		w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: timeTriggerTick{generation: generation}})
	}
}

func timeTriggerNextDelay(now time.Time, config timeTriggerConfig) (time.Duration, bool) {
	switch config.mode {
	case timeTriggerModeInterval:
		delay, err := config.intervalDuration()
		return delay, err == nil
	case timeTriggerModeDaily:
		seconds, err := parseTimeTriggerDailyTime(config.dailyTime)
		if err != nil {
			return 0, false
		}
		return nextTimeTriggerDailyDelay(now, seconds), true
	default:
		return 0, false
	}
}

func parseTimeTriggerDailyTime(value string) (int, error) {
	value = strings.TrimSpace(value)
	layout := "15:04"
	if strings.Count(value, ":") == 2 {
		layout = "15:04:05"
	}
	parsed, err := time.Parse(layout, value)
	if err != nil {
		return 0, errTimeTriggerDailyTime
	}
	return parsed.Hour()*3600 + parsed.Minute()*60 + parsed.Second(), nil
}

func ParseTimeTriggerDailyTime(value string) (int, error) {
	return parseTimeTriggerDailyTime(value)
}

func nextTimeTriggerDailyDelay(now time.Time, seconds int) time.Duration {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	next := midnight.Add(time.Duration(seconds) * time.Second)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

func NextTimeTriggerDailyDelay(now time.Time, seconds int) time.Duration {
	return nextTimeTriggerDailyDelay(now, seconds)
}

func (n *timeTriggerNode) emitAll() {
	n.ForEachPortLink(n.out, func(link uint64) {
		n.W.EmitEventLocked(n.ID, link, std.Trigger{})
	})
}

func (n *timeTriggerNode) updateLabel() error {
	return n.SetLabel(n.config.label())
}

func (n *timeTriggerNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *timeTriggerNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *timeTriggerNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, n.validateConfigField(msg))
}

func (n *timeTriggerNode) validateConfigField(msg formular.FieldValidateMessage) error {
	next := n.config
	switch msg.Field.FieldID {
	case "mode":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.mode = value
		}
		next.normalize()
		return next.validate()
	case "interval":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.interval = value
		}
		next.normalize()
		_, err := next.intervalDuration()
		return err
	case "dailyTime":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.dailyTime = value
		}
		_, err := parseTimeTriggerDailyTime(next.dailyTime)
		return err
	case "emitOnReady":
		if value, ok := formular.BoolValue(msg.Value); ok {
			next.emitOnReady = value
		}
	default:
		return fmt.Errorf("unknown field %q", msg.Field.FieldID)
	}
	next.normalize()
	return next.validate()
}

func (n *timeTriggerNode) configBlock() formular.Block {
	return timeTriggerConfigBlock(n.config, n.formError)
}

func timeTriggerConfigBlock(config timeTriggerConfig, formError string) formular.Block {
	statusErr := formError != ""
	status := []formular.FieldOption{formular.Validation}
	if statusErr {
		status = append(status, formular.Status(formular.StatusError), formular.StatusText(formError))
	}
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		formular.RadioField("mode", "Mode", config.mode, []formular.Choice{
			{Value: timeTriggerModeDisabled, Label: "Disabled"},
			{Value: timeTriggerModeInterval, Label: "Interval"},
			{Value: timeTriggerModeDaily, Label: "Daily"},
		}, status...),
		formular.TextField("interval", "Every", config.interval, append(status, formular.Placeholder("2m"))...),
		formular.TextField("dailyTime", "At", config.dailyTime, append(status, formular.Placeholder("00:00"))...),
		formular.CheckboxField("emitOnReady", "Trigger on ready", config.emitOnReady),
	}}
}
