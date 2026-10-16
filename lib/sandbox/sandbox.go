package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
)

type TemplateID string
type ProgramID string
type Root uint8

const (
	Temporary Root = iota
	Persistent
)

type Path struct {
	Root Root
	Rel  string
}

// Owner is assigned by the engine. Name is a label, not a storage identity.
type Owner struct {
	Workspace    string
	Key          string
	Epoch        uint64
	Class        string
	Name         string
	NameRevision uint64
}
type OwnerRename struct {
	Owner       Owner
	NewName     string
	OperationID string
}
type Service interface {
	BindOwner(context.Context, Owner) (OwnerSandboxes, error)
	RebindOwner(context.Context, OwnerRename) (Owner, error)
	ReleaseOwner(context.Context, Owner) error
	Close() error
}

// OwnerIssuer is a host-only extension used by the engine when a logical node
// first needs sandboxes. Nodes never receive this interface. A nonempty Key is
// restored metadata and must already be authorized by the durable registry; an
// empty Key requests a new unguessable identity.
type OwnerIssuer interface {
	IssueOwner(context.Context, Owner) (Owner, error)
}

// OwnerBinder combines identity issue/restore with binding so a canceled bind
// cannot leave a newly issued durable owner active without an engine handle.
type OwnerBinder interface {
	IssueAndBindOwner(context.Context, Owner) (Owner, OwnerSandboxes, error)
}

// OwnerRevoker lets the engine revoke live capabilities synchronously without
// waiting for native process cleanup in a workspace notification callback.
type OwnerRevoker interface {
	RevokeOwner(context.Context, Owner) error
}
type OwnerSandboxes interface {
	Acquire(context.Context, string, TemplateID) (Sandbox, error)
	Remove(context.Context, string) error
}
type Sandbox interface {
	Status() Status
	FS(Root) (Files, error)
	ChildPath(Path) (string, error)
	ProgramPath(ProgramID) (string, error)
	ProcessContext() ProcessContext
	NewCommand(ProgramID, Launch) (Command, error)
	DialContext(context.Context, string, string) (net.Conn, error)
	Listen(context.Context, string, string) (net.Listener, error)
	ListenPacket(context.Context, string, string) (net.PacketConn, error)
	DialUnix(context.Context, Path) (net.Conn, error)
	ListenUnix(context.Context, Path) (net.Listener, error)
	Done() <-chan struct{}
	Err() error
	Close() error
}
type ProcessContext struct {
	Platform      string
	ControllerPID int
}
type Launch struct {
	Inputs    map[string]Value
	Directory Path
}
type ValueKind string

const (
	TextValue     ValueKind = "text"
	IntegerValue  ValueKind = "integer"
	BooleanValue  ValueKind = "boolean"
	PathValue     ValueKind = "path"
	ProgramValue  ValueKind = "program"
	EndpointValue ValueKind = "endpoint"
)

type Value struct {
	Kind     ValueKind
	Text     string
	Int      int64
	Bool     bool
	Path     Path
	Program  ProgramID
	Endpoint string
}
type Signal string

const (
	Interrupt Signal = "interrupt"
	Terminate Signal = "terminate"
	Kill      Signal = "kill"
)

type Command interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Start(context.Context) error
	Wait() (Exit, error)
	Signal(Signal) error
	Kill() error
	Done() <-chan struct{}
}
type Exit struct {
	Code   int
	Signal Signal
	Killed bool
}
type ExitError struct{ Exit Exit }

func (e *ExitError) Error() string {
	return fmt.Sprintf("sandbox command exited with code %d (signal %q, killed %t)", e.Exit.Code, e.Exit.Signal, e.Exit.Killed)
}

type OpenOptions struct {
	Read, Write, Create, Exclusive, Truncate, Append bool
	Mode                                             fs.FileMode
}
type Files interface {
	MkdirAll(context.Context, string, fs.FileMode) error
	TempDir(context.Context, string, string) (string, error)
	Open(context.Context, string, OpenOptions) (File, error)
	ReadFile(context.Context, string, int64) ([]byte, error)
	WriteFile(context.Context, string, []byte, fs.FileMode) error
	Rename(context.Context, string, string) error
	Stat(context.Context, string) (fs.FileInfo, error)
	ReadDir(context.Context, string, int) ([]fs.DirEntry, error)
	Remove(context.Context, string) error
	RemoveAll(context.Context, string) error
}
type File interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Stat() (fs.FileInfo, error)
	Sync() error
}

var (
	ErrDenied      = errors.New("sandbox policy denied")
	ErrUnsupported = errors.New("sandbox operation unsupported")
	ErrUnavailable = errors.New("sandbox feature unavailable")
	ErrClosed      = errors.New("sandbox owner closed")
	ErrStale       = errors.New("sandbox generation stale")
	ErrConflict    = errors.New("sandbox operation conflict")
	ErrQuota       = errors.New("sandbox quota exceeded")
	ErrLaunch      = errors.New("sandbox launch failed")
	ErrInvalid     = errors.New("invalid sandbox request")
)
