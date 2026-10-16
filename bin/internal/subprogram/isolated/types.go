package isolated

import (
	"io/fs"
	"time"

	"github.com/asciimoth/almagest/lib/sandbox"
)

const (
	opServiceStatus  = "service_status"
	opIssueBind      = "issue_bind_owner"
	opBind           = "bind_owner"
	opRebind         = "rebind_owner"
	opRelease        = "release_owner"
	opRevoke         = "revoke_owner"
	opAcquire        = "acquire"
	opRemove         = "remove"
	opSandboxStatus  = "sandbox_status"
	opSandboxClose   = "sandbox_close"
	opSandboxWait    = "sandbox_wait"
	opChildPath      = "child_path"
	opProgramPath    = "program_path"
	opProcess        = "process_context"
	opNewCommand     = "new_command"
	opCommandUpload  = "command_upload"
	opCommandChunk   = "command_chunk"
	opCommandCommit  = "command_commit"
	opCommandAbort   = "command_abort"
	opCommandPipe    = "command_pipe"
	opCommandStart   = "command_start"
	opCommandWait    = "command_wait"
	opCommandSignal  = "command_signal"
	opCommandKill    = "command_kill"
	opStreamRead     = "stream_read"
	opStreamWrite    = "stream_write"
	opStreamClose    = "stream_close"
	opFS             = "fs"
	opFileOpen       = "file_open"
	opFileRead       = "file_read"
	opFileWrite      = "file_write"
	opFileSeek       = "file_seek"
	opFileStat       = "file_stat"
	opFileSync       = "file_sync"
	opFileClose      = "file_close"
	opDial           = "dial"
	opListen         = "listen"
	opListenPacket   = "listen_packet"
	opDialUnix       = "dial_unix"
	opListenUnix     = "listen_unix"
	opConnRead       = "conn_read"
	opConnWrite      = "conn_write"
	opConnDeadline   = "conn_deadline"
	opConnClose      = "conn_close"
	opAccept         = "accept"
	opListenerClose  = "listener_close"
	opPacketRead     = "packet_read"
	opPacketWrite    = "packet_write"
	opPacketDeadline = "packet_deadline"
	opPacketClose    = "packet_close"
)

var operations = map[string]bool{
	opServiceStatus: true, opIssueBind: true, opBind: true, opRebind: true, opRelease: true, opRevoke: true,
	opAcquire: true, opRemove: true, opSandboxStatus: true, opSandboxClose: true, opSandboxWait: true,
	opChildPath: true, opProgramPath: true, opProcess: true, opNewCommand: true, opCommandPipe: true,
	opCommandUpload: true, opCommandChunk: true, opCommandCommit: true, opCommandAbort: true,
	opCommandStart: true, opCommandWait: true, opCommandSignal: true, opCommandKill: true, opFS: true,
	opStreamRead: true, opStreamWrite: true, opStreamClose: true,
	opFileOpen: true, opFileRead: true, opFileWrite: true, opFileSeek: true, opFileStat: true,
	opFileSync: true, opFileClose: true, opDial: true, opListen: true, opListenPacket: true,
	opDialUnix: true, opListenUnix: true, opConnRead: true, opConnWrite: true, opConnDeadline: true,
	opConnClose: true, opAccept: true, opListenerClose: true, opPacketRead: true, opPacketWrite: true,
	opPacketDeadline: true, opPacketClose: true,
}

func validOperation(operation string) bool { return operations[operation] }

type empty struct{}
type idRequest struct {
	ID uint64 `json:"id"`
}
type idResponse struct {
	ID uint64 `json:"id"`
}

type ownerRequest struct {
	Owner sandbox.Owner `json:"owner"`
	IDs   []uint64      `json:"ids,omitempty"`
}
type ownerResponse struct {
	Owner sandbox.Owner `json:"owner"`
	ID    uint64        `json:"id"`
}
type rebindRequest struct {
	Rename sandbox.OwnerRename `json:"rename"`
}
type acquireRequest struct {
	OwnerID  uint64             `json:"owner_id"`
	Slot     string             `json:"slot"`
	Template sandbox.TemplateID `json:"template"`
}
type removeRequest struct {
	OwnerID uint64 `json:"owner_id"`
	Slot    string `json:"slot"`
}
type pathRequest struct {
	SandboxID uint64       `json:"sandbox_id"`
	Path      sandbox.Path `json:"path"`
}
type programRequest struct {
	SandboxID uint64            `json:"sandbox_id"`
	Program   sandbox.ProgramID `json:"program"`
}
type stringResponse struct {
	Value string `json:"value"`
}
type processResponse struct {
	Value sandbox.ProcessContext `json:"value"`
}
type statusResponse struct {
	Value sandbox.Status `json:"value"`
}
type serviceStatusResponse struct {
	Value sandbox.ServiceStatus `json:"value"`
}

type newCommandRequest struct {
	SandboxID uint64            `json:"sandbox_id"`
	Program   sandbox.ProgramID `json:"program"`
	Launch    sandbox.Launch    `json:"launch"`
}
type commandUploadRequest struct {
	SandboxID uint64            `json:"sandbox_id"`
	Program   sandbox.ProgramID `json:"program"`
	Size      uint32            `json:"size"`
	Digest    [32]byte          `json:"digest"`
}
type commandChunkRequest struct {
	UploadID uint64 `json:"upload_id"`
	Offset   uint32 `json:"offset"`
	Data     []byte `json:"data"`
}
type commandPipeRequest struct {
	CommandID uint64 `json:"command_id"`
	Kind      string `json:"kind"`
}
type commandStartRequest struct {
	CommandID uint64 `json:"command_id"`
}
type commandWaitResponse struct {
	Exit      sandbox.Exit `json:"exit"`
	ExitError bool         `json:"exit_error"`
}
type commandSignalRequest struct {
	CommandID uint64         `json:"command_id"`
	Signal    sandbox.Signal `json:"signal"`
}

type fsRequest struct {
	SandboxID uint64       `json:"sandbox_id"`
	Root      sandbox.Root `json:"root"`
	Action    string       `json:"action"`
	Name      string       `json:"name"`
	Other     string       `json:"other,omitempty"`
	Pattern   string       `json:"pattern,omitempty"`
	Mode      uint32       `json:"mode,omitempty"`
	Maximum   int64        `json:"maximum,omitempty"`
	Data      []byte       `json:"data,omitempty"`
}
type fsResponse struct {
	Name    string         `json:"name,omitempty"`
	Data    []byte         `json:"data,omitempty"`
	Info    wireFileInfo   `json:"info,omitempty"`
	Entries []wireDirEntry `json:"entries,omitempty"`
}
type fileOpenRequest struct {
	SandboxID uint64              `json:"sandbox_id"`
	Root      sandbox.Root        `json:"root"`
	Name      string              `json:"name"`
	Options   sandbox.OpenOptions `json:"options"`
}
type fileIORequest struct {
	ID     uint64 `json:"id"`
	Data   []byte `json:"data,omitempty"`
	Count  int    `json:"count,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Whence int    `json:"whence,omitempty"`
}
type dataResponse struct {
	Data  []byte `json:"data,omitempty"`
	Count int    `json:"count"`
}
type seekResponse struct {
	Offset int64 `json:"offset"`
}

type wireFileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	ModUnix int64  `json:"mod_unix"`
	Dir     bool   `json:"dir"`
}

func makeWireFileInfo(info fs.FileInfo) wireFileInfo {
	return wireFileInfo{Name: info.Name(), Size: info.Size(), Mode: uint32(info.Mode()), ModUnix: info.ModTime().UnixNano(), Dir: info.IsDir()}
}

type wireDirEntry struct {
	Name string       `json:"name"`
	Type uint32       `json:"type"`
	Info wireFileInfo `json:"info"`
}

type networkRequest struct {
	SandboxID uint64       `json:"sandbox_id"`
	Network   string       `json:"network,omitempty"`
	Address   string       `json:"address,omitempty"`
	Path      sandbox.Path `json:"path"`
}
type networkResponse struct {
	ID     uint64   `json:"id"`
	Local  wireAddr `json:"local"`
	Remote wireAddr `json:"remote"`
}
type wireAddr struct {
	Network string `json:"network"`
	Value   string `json:"value"`
}
type deadlineRequest struct {
	ID       uint64 `json:"id"`
	Kind     string `json:"kind"`
	UnixNano int64  `json:"unix_nano"`
}
type packetReadResponse struct {
	Data []byte   `json:"data"`
	Addr wireAddr `json:"addr"`
}
type packetWriteRequest struct {
	ID   uint64   `json:"id"`
	Data []byte   `json:"data"`
	Addr wireAddr `json:"addr"`
}

func wireTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value)
}
