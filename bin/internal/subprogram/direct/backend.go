// Package direct implements the daemon-owned in-process policy service and a
// separate native child process boundary for direct daemon mode. It never
// changes the daemon's credentials or namespaces.
package direct

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

type Backend struct {
	policy         policy.Config
	probe          policy.Probe
	programInfo    map[sandbox.ProgramID]os.FileInfo
	readOnlyInfo   map[sandbox.TemplateID][]os.FileInfo
	launcherInfo   os.FileInfo
	fullAvailable  bool
	mu             sync.Mutex
	activeCommands int
}

type capabilityUnavailableError struct{ reason string }

const mappedReadOnlyUnavailableReason = "mapped read-only targets require an exact filesystem-view backend"

func (e capabilityUnavailableError) Error() string {
	return sandbox.ErrUnavailable.Error() + ": " + e.reason
}
func (e capabilityUnavailableError) Unwrap() error       { return sandbox.ErrUnavailable }
func (e capabilityUnavailableError) PublicError() string { return e.reason }

func protectionPlan(mode sandbox.Mode, probe policy.Probe, fullAvailable bool, template policy.Template) (sandbox.Status, uint32, uint32, bool, bool, error) {
	var uid, gid uint32
	native, full := false, false
	if template.RuntimeProfile == "linux-landlock-v1" && mode != sandbox.Off {
		probe.Available = maps.Clone(probe.Available)
		probe.Reasons = maps.Clone(probe.Reasons)
		if probe.Available == nil {
			probe.Available = make(map[sandbox.Feature]bool)
		}
		if probe.Reasons == nil {
			probe.Reasons = make(map[sandbox.Feature]string)
		}
		prerequisiteErr := nativePrerequisites()
		var privilege bool
		if prerequisiteErr == nil {
			uid, gid, privilege, prerequisiteErr = nativeIdentity(template.Linux)
		}
		if prerequisiteErr == nil && probe.Platform == "linux" && runtime.GOARCH == "amd64" {
			native = true
			// Template status describes the identity that the launcher installs,
			// not the daemon identity that performed the host probe.
			probe.EffectiveIdentity = fmt.Sprintf("%d:%d", uid, gid)
			probe.Available[sandbox.WritableStorage] = true
			probe.Available[sandbox.FilesystemAccess] = true
			if privilege {
				probe.Available[sandbox.PrivilegeRemoval] = true
			}
			if fullAvailable {
				full = true
				for _, feature := range []sandbox.Feature{sandbox.PrivilegeRemoval, sandbox.Environment,
					sandbox.HandleInheritance, sandbox.WritableStorage, sandbox.FilesystemAccess,
					sandbox.ExactFilesystemView, sandbox.PrivateNetwork, sandbox.ProcessIsolation,
					sandbox.ProcessTree, sandbox.ResourceLimits} {
					probe.Available[feature] = true
				}
			}
		} else if prerequisiteErr != nil && !errors.Is(prerequisiteErr, sandbox.ErrUnavailable) {
			return sandbox.Status{}, 0, 0, false, false, prerequisiteErr
		} else {
			uid, gid = 0, 0
			for _, feature := range []sandbox.Feature{sandbox.WritableStorage, sandbox.FilesystemAccess, sandbox.PrivilegeRemoval} {
				if probe.Reasons[feature] == "" {
					probe.Reasons[feature] = "linux-native-prerequisite-unavailable"
				}
			}
		}
	}
	if template.RuntimeProfile == "windows-native-v1" && mode != sandbox.Off {
		probe.Available = maps.Clone(probe.Available)
		probe.Reasons = maps.Clone(probe.Reasons)
		if probe.Available == nil {
			probe.Available = make(map[sandbox.Feature]bool)
		}
		if probe.Reasons == nil {
			probe.Reasons = make(map[sandbox.Feature]string)
		}
		if prerequisiteErr := windowsNativePrerequisites(template); prerequisiteErr == nil && probe.Platform == "windows" {
			native = true
			for _, feature := range []sandbox.Feature{sandbox.PrivilegeRemoval, sandbox.Environment,
				sandbox.HandleInheritance, sandbox.WritableStorage, sandbox.ProcessIsolation,
				sandbox.ProcessTree, sandbox.ResourceLimits} {
				probe.Available[feature] = true
			}
		} else if prerequisiteErr != nil && !errors.Is(prerequisiteErr, sandbox.ErrUnavailable) {
			return sandbox.Status{}, 0, 0, false, false, prerequisiteErr
		} else {
			for _, feature := range []sandbox.Feature{sandbox.PrivilegeRemoval, sandbox.WritableStorage,
				sandbox.ProcessIsolation, sandbox.ProcessTree, sandbox.ResourceLimits} {
				if probe.Reasons[feature] == "" {
					probe.Reasons[feature] = "windows-native-prerequisite-unavailable"
				}
			}
		}
	}
	status, err := policy.Select(mode, probe, template.RequiredFeatures)
	status.Profile = template.RuntimeProfile
	if template.RuntimeProfile == "linux-landlock-v1" && !native {
		status.Profile = "direct-v1"
	} else if full {
		status.Profile = "linux-full-v1"
	} else if template.RuntimeProfile == "windows-native-v1" && !native {
		status.Profile = "direct-v1"
	}
	return status, uid, gid, native, full, err
}

func NewBackend(config policy.Config, probe policy.Probe) (*Backend, error) {
	normalized, err := policy.Normalize(config)
	if err != nil {
		return nil, err
	}
	if probe.Platform != runtime.GOOS {
		return nil, fmt.Errorf("%w: probe platform %q does not match %q", sandbox.ErrInvalid, probe.Platform, runtime.GOOS)
	}
	for feature, available := range probe.Available {
		if available && feature != sandbox.Environment && feature != sandbox.HandleInheritance {
			return nil, fmt.Errorf("%w: unqualified direct-backend feature %q", sandbox.ErrInvalid, feature)
		}
	}
	if err := prepareRoot(normalized.TemporaryRoot); err != nil {
		return nil, fmt.Errorf("temporary root: %w", err)
	}
	if err := prepareRoot(normalized.RegistryRoot); err != nil {
		return nil, fmt.Errorf("registry root: %w", err)
	}
	var launcherInfo os.FileInfo
	if normalized.LinuxLauncher != "" {
		launcherInfo, err = validateExecutable("Linux launcher", normalized.LinuxLauncher)
		if err != nil {
			return nil, err
		}
	}
	readOnlyInfo := make(map[sandbox.TemplateID][]os.FileInfo, len(normalized.Templates))
	fullAvailable := fullNativePrerequisites(normalized.LinuxLauncher, normalized.LinuxCgroupRoot) == nil &&
		probeFullNativeLauncher(normalized.LinuxLauncher, launcherInfo) == nil
	preparedTemporaryParent := false
	for id, template := range normalized.Templates {
		_, uid, gid, _, full, planErr := protectionPlan(normalized.Mode, probe, fullAvailable, template)
		if planErr != nil {
			return nil, fmt.Errorf("template %q preflight: %w", id, planErr)
		}
		if normalized.Mode == sandbox.Strict && !full {
			for _, input := range template.ReadOnly {
				if filepath.Clean(input.Source) != filepath.Clean(input.Target) {
					return nil, fmt.Errorf("template %q: %w: %s", id, sandbox.ErrUnavailable, mappedReadOnlyUnavailableReason)
				}
			}
		}
		if uid != 0 && !preparedTemporaryParent {
			if err := prepareNativeStorageParent(normalized.TemporaryRoot, uid, gid); err != nil {
				return nil, fmt.Errorf("temporary root traversal: %w", err)
			}
			preparedTemporaryParent = true
		}
		if template.PersistentRoot != "" {
			if err := prepareRoot(template.PersistentRoot); err != nil {
				return nil, fmt.Errorf("template %q persistent root: %w", id, err)
			}
			if err := prepareNativeStorageParent(template.PersistentRoot, uid, gid); err != nil {
				return nil, fmt.Errorf("template %q persistent root traversal: %w", id, err)
			}
		}
		for index, input := range template.ReadOnly {
			heldInfo, validateErr := validateReadOnlySource(input.Source)
			if validateErr != nil {
				return nil, fmt.Errorf("template %q read-only input %d: %w", id, index, validateErr)
			}
			if input.Executable && !heldInfo.Mode().IsRegular() {
				return nil, fmt.Errorf("template %q read-only input %d: %w: executable input must be a regular file", id, index, sandbox.ErrInvalid)
			}
			readOnlyInfo[id] = append(readOnlyInfo[id], heldInfo)
		}
	}
	programInfo := make(map[sandbox.ProgramID]os.FileInfo, len(normalized.Programs))
	for id, program := range normalized.Programs {
		programInfo[id], err = validateExecutable(fmt.Sprintf("program %q", id), program.Executable)
		if err != nil {
			return nil, err
		}
	}
	return &Backend{policy: normalized, probe: probe, programInfo: programInfo, readOnlyInfo: readOnlyInfo, launcherInfo: launcherInfo, fullAvailable: fullAvailable}, nil
}

func validateReadOnlySource(source string) (os.FileInfo, error) {
	if err := validatePathComponents(source); err != nil {
		return nil, err
	}
	var root os.FileInfo
	entries := 0
	err := filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 100000 {
			return sandbox.ErrQuota
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
			return sandbox.ErrDenied
		}
		file, err := safeOpen(name)
		if err != nil {
			return err
		}
		held, statErr := file.Stat()
		aliases := statErr == nil && unsafeTrustedAlias(file, held)
		closeErr := file.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(info, held) || aliases {
			return errors.Join(sandbox.ErrDenied, statErr, closeErr)
		}
		if name == source {
			root = held
		}
		return nil
	})
	if err != nil || root == nil {
		return nil, errors.Join(sandbox.ErrInvalid, err)
	}
	if err := validatePathComponents(source); err != nil {
		return nil, err
	}
	return root, nil
}

func validateExecutable(label, path string) (os.FileInfo, error) {
	if err := validatePathComponents(path); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || runtime.GOOS != "windows" && (info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0) {
		return nil, fmt.Errorf("%w: %s is not an executable regular file", sandbox.ErrInvalid, label)
	}
	file, err := safeOpen(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	heldInfo, statErr := file.Stat()
	multipleLinks := statErr == nil && unsafeTrustedAlias(file, heldInfo)
	closeErr := file.Close()
	if statErr != nil || closeErr != nil {
		return nil, fmt.Errorf("%s identity: %w", label, errors.Join(statErr, closeErr))
	}
	if !os.SameFile(info, heldInfo) || multipleLinks {
		return nil, fmt.Errorf("%w: %s has an unsafe identity alias", sandbox.ErrInvalid, label)
	}
	if err := validatePathComponents(path); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return heldInfo, nil
}

func (b *Backend) ServiceStatus() sandbox.ServiceStatus {
	result := sandbox.ServiceStatus{Mode: b.policy.Mode, Platform: b.probe.Platform, EffectiveIdentity: b.probe.EffectiveIdentity,
		Templates: make(map[sandbox.TemplateID]sandbox.Status, len(b.policy.Templates)+len(b.policy.UnavailableTemplates)), Limits: map[string]int64{
			"instances": int64(b.policy.Limits.Instances), "commands": int64(b.policy.Limits.Commands),
			"relay_bytes": b.policy.Limits.RelayBytes, "receipts": int64(b.policy.Limits.Receipts),
		}}
	for id, template := range b.policy.Templates {
		status, _, _, _, _, _ := protectionPlan(b.policy.Mode, b.probe, b.fullAvailable, template)
		status.Template = id
		result.Templates[id] = status
	}
	for id := range b.policy.UnavailableTemplates {
		if _, exists := result.Templates[id]; !exists {
			result.Templates[id] = sandbox.Status{Template: id, Mode: b.policy.Mode, State: sandbox.Failed}
		}
	}
	return result
}

func prepareRoot(path string) error {
	if err := validatePathComponents(path); err != nil {
		return err
	}
	created := false
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		created = true
	} else if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: root is not a real directory", sandbox.ErrInvalid)
	}
	if err := validatePathComponents(path); err != nil {
		return err
	}
	if created {
		return os.Chmod(path, 0o700)
	}
	// The native backend changes an owned root to 0711 so that its unprivileged
	// child identity can traverse it without listing its contents.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 && info.Mode().Perm() != 0o711 {
		return fmt.Errorf("%w: existing root must be private or use native traversal permissions", sandbox.ErrDenied)
	}
	return nil
}

// validatePathComponents rejects authority paths that traverse a symbolic
// link. Lexical overlap checks are safe only when an ancestor cannot redirect
// a protected path into another tree.
func validatePathComponents(name string) error {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return sandbox.ErrInvalid
	}
	volume := filepath.VolumeName(name)
	remainder := strings.TrimPrefix(name[len(volume):], string(filepath.Separator))
	current := volume + string(filepath.Separator)
	parts := strings.Split(remainder, string(filepath.Separator))
	for index, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || pathComponentRedirects(info) {
			return fmt.Errorf("%w: path traverses a link or reparse point", sandbox.ErrDenied)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("%w: path ancestor is not a directory", sandbox.ErrInvalid)
		}
	}
	return nil
}

func (b *Backend) Prepare(ctx context.Context, owner sandbox.Owner, slot string, templateID sandbox.TemplateID, generation uint64) (sandbox.Sandbox, error) {
	if reason := b.policy.UnavailableTemplates[templateID]; reason != "" {
		return nil, capabilityUnavailableError{reason: reason}
	}
	template, ok := b.policy.Templates[templateID]
	if !ok || !slices.Contains(template.AllowedClasses, owner.Class) {
		return nil, sandbox.ErrDenied
	}
	status, uid, gid, native, full, err := protectionPlan(b.policy.Mode, b.probe, b.fullAvailable, template)
	if err != nil {
		return nil, err
	}
	status.Owner = owner
	status.Template = templateID
	status.Generation = generation
	status.Limits = map[string]int64{
		"commands":        int64(template.Limits.Commands),
		"tasks":           int64(template.Limits.Tasks),
		"files":           int64(template.Limits.Files),
		"sockets":         int64(template.Limits.Sockets),
		"memory_bytes":    template.Limits.MemoryBytes,
		"cpu_percent":     int64(template.Limits.CPUPercent),
		"transfer_bytes":  template.Limits.TransferBytes,
		"temporary_bytes": template.Limits.TemporaryBytes,
		"output_bytes":    template.Limits.OutputBytes,
	}
	if err := policy.Verify(status, appliedFeatures(status)); err != nil {
		return nil, err
	}

	instanceName := storageName(owner.Workspace, owner.Key, slot)
	temporary := filepath.Join(b.policy.TemporaryRoot, instanceName+fmt.Sprintf("-%d", generation))
	temporaryParent, err := os.OpenRoot(b.policy.TemporaryRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: temporary parent: %v", sandbox.ErrUnavailable, err)
	}
	defer func() { _ = temporaryParent.Close() }()
	temporaryName := filepath.Base(temporary)
	if err := temporaryParent.Mkdir(temporaryName, 0o700); err != nil {
		return nil, fmt.Errorf("%w: temporary storage: %v", sandbox.ErrUnavailable, err)
	}
	temporaryRoot, temporaryInfo, err := openConfinedChild(temporaryParent, temporaryName)
	if err != nil {
		return nil, fmt.Errorf("%w: temporary storage identity: %v", sandbox.ErrUnavailable, err)
	}
	if err = prepareNativeStorage(temporary, uid, gid); err != nil {
		_ = temporaryRoot.Close()
		return nil, fmt.Errorf("%w: temporary storage identity: %v", sandbox.ErrUnavailable, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temporary)
		}
	}()
	var persistent string
	var persistentRoot *confinedRoot
	var persistentInfo os.FileInfo
	if template.PersistentRoot != "" {
		persistentParent, openErr := os.OpenRoot(template.PersistentRoot)
		if openErr != nil {
			_ = temporaryRoot.Close()
			return nil, fmt.Errorf("%w: persistent parent: %v", sandbox.ErrUnavailable, openErr)
		}
		defer func() { _ = persistentParent.Close() }()
		ownerName := storageName(owner.Workspace, owner.Key)
		if err := persistentParent.MkdirAll(ownerName, 0o700); err != nil {
			_ = temporaryRoot.Close()
			return nil, fmt.Errorf("%w: persistent owner storage: %v", sandbox.ErrUnavailable, err)
		}
		ownerPath := filepath.Join(template.PersistentRoot, ownerName)
		if err := prepareNativeStorageParent(ownerPath, uid, gid); err != nil {
			_ = temporaryRoot.Close()
			return nil, fmt.Errorf("%w: persistent owner traversal: %v", sandbox.ErrUnavailable, err)
		}
		ownerRoot, _, openErr := openConfinedChild(persistentParent, ownerName)
		if openErr != nil {
			_ = temporaryRoot.Close()
			return nil, fmt.Errorf("%w: persistent owner identity: %v", sandbox.ErrUnavailable, openErr)
		}
		defer func() { _ = ownerRoot.Close() }()
		slotName := storageName(slot)
		if err := ownerRoot.root.MkdirAll(slotName, 0o700); err != nil {
			_ = temporaryRoot.Close()
			return nil, fmt.Errorf("%w: persistent storage: %v", sandbox.ErrUnavailable, err)
		}
		persistentRoot, persistentInfo, err = openConfinedChild(ownerRoot.root, slotName)
		if err != nil {
			_ = temporaryRoot.Close()
			return nil, err
		}
		persistent = filepath.Join(template.PersistentRoot, ownerName, slotName)
		if err = prepareNativeStorage(persistent, uid, gid); err != nil {
			_ = persistentRoot.Close()
			_ = temporaryRoot.Close()
			return nil, fmt.Errorf("%w: persistent storage identity: %v", sandbox.ErrUnavailable, err)
		}
	}

	lifetime, cancel := context.WithCancelCause(ctx)
	instance := &instance{
		backend:        b,
		owner:          owner,
		template:       template,
		templateID:     templateID,
		slot:           slot,
		nativeUID:      uid,
		nativeGID:      gid,
		nativeProfile:  native && template.RuntimeProfile == "linux-landlock-v1",
		nativeFull:     full,
		status:         status,
		ctx:            lifetime,
		cancel:         cancel,
		done:           make(chan struct{}),
		temporaryPath:  temporary,
		temporaryName:  temporaryName,
		temporaryInfo:  temporaryInfo,
		persistentPath: persistent,
		persistentInfo: persistentInfo,
		roots: map[sandbox.Root]*confinedRoot{
			sandbox.Temporary: temporaryRoot,
		},
		commands: make(map[*command]struct{}),
		pending:  make(map[*command]struct{}),
		sockets:  make(map[any]func() error),
		files:    make(map[*trackedFile]struct{}),
	}
	if persistentRoot != nil {
		instance.roots[sandbox.Persistent] = persistentRoot
	}
	if full {
		if err = instance.startNativeWorker(); err != nil {
			_ = instance.closeNativeWorker()
			_ = persistentRootClose(persistentRoot)
			_ = temporaryRoot.Close()
			return nil, err
		}
	}
	if template.RuntimeProfile == "windows-native-v1" && native {
		if err = instance.startWindowsBoundary(); err != nil {
			_ = persistentRootClose(persistentRoot)
			_ = temporaryRoot.Close()
			return nil, err
		}
	}
	status.State = sandbox.Ready
	instance.status = status
	cleanup = false
	go func() {
		<-lifetime.Done()
		_ = instance.Close()
	}()
	return instance, nil
}

func appliedFeatures(status sandbox.Status) []sandbox.Feature {
	result := make([]sandbox.Feature, 0, len(status.Features))
	for _, feature := range status.Features {
		if feature.State == sandbox.Applied {
			result = append(result, feature.Feature)
		}
	}
	return result
}

func storageName(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	// A 128-bit opaque name keeps filesystem Unix-socket paths below the common
	// 108-byte native limit while retaining ample collision resistance.
	return hex.EncodeToString(hash[:16])
}

// PurgeOwner removes only state covered by a template's protected deletion
// policy. Service calls it after the manager has verified instance cleanup.
func (b *Backend) PurgeOwner(owner sandbox.Owner) error {
	ownerName := storageName(owner.Workspace, owner.Key)
	var result error
	for id, template := range b.policy.Templates {
		if !template.DeleteStateOnOwnerRemoval || template.PersistentRoot == "" {
			continue
		}
		root, err := os.OpenRoot(template.PersistentRoot)
		if err == nil {
			err = root.RemoveAll(ownerName)
			err = errors.Join(err, root.Close())
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("template %q owner state: %w", id, err))
		}
	}
	return result
}

type instance struct {
	backend              *Backend
	owner                sandbox.Owner
	template             policy.Template
	templateID           sandbox.TemplateID
	slot                 string
	nativeUID, nativeGID uint32
	nativeProfile        bool
	nativeFull           bool
	nativeBoundary       *nativeInstanceBoundary
	nativeWorker         *exec.Cmd
	nativeWorkerDone     chan struct{}
	nativeWorkerWaitErr  error
	nativeWorkerLife     *os.File
	nativeWorkerRoot     *os.File
	nativeNamespaces     map[string]*os.File
	nativeMountName      string
	windowsBoundary      *windowsInstanceBoundary
	nextCommand          uint64

	mu             sync.Mutex
	status         sandbox.Status
	err            error
	ctx            context.Context
	cancel         context.CancelCauseFunc
	done           chan struct{}
	closeOnce      sync.Once
	temporaryPath  string
	temporaryName  string
	temporaryInfo  os.FileInfo
	persistentPath string
	persistentInfo os.FileInfo
	roots          map[sandbox.Root]*confinedRoot
	commands       map[*command]struct{}
	pending        map[*command]struct{}
	sockets        map[any]func() error
	files          map[*trackedFile]struct{}
}

var _ sandbox.Sandbox = (*instance)(nil)

func (s *instance) Status() sandbox.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Clone()
}

func (s *instance) FS(root sandbox.Root) (sandbox.Files, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != sandbox.Ready {
		return nil, sandbox.ErrClosed
	}
	value := s.roots[root]
	if value == nil {
		return nil, sandbox.ErrUnavailable
	}
	return &files{instance: s, root: value, rootID: root}, nil
}

func (s *instance) ChildPath(path sandbox.Path) (string, error) {
	if err := sandbox.ValidatePath(path, true); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.childPathLocked(path)
}

// childPathLocked resolves an already validated path while s.mu is held.
// Launch rendering runs below NewCommand's instance lock, so it must not call
// the public ChildPath method and enter that lock a second time.
func (s *instance) childPathLocked(path sandbox.Path) (string, error) {
	if s.status.State != sandbox.Ready {
		return "", sandbox.ErrClosed
	}
	if s.roots[path.Root] == nil {
		return "", sandbox.ErrUnavailable
	}
	if !s.nativeFull {
		return s.hostPath(path)
	}
	base := "/almagest/temp"
	if path.Root == sandbox.Persistent {
		base = "/almagest/state"
	}
	if path.Rel == "" || path.Rel == "." {
		return base, nil
	}
	return base + "/" + path.Rel, nil
}

func (s *instance) ProgramPath(id sandbox.ProgramID) (string, error) {
	if reason := s.backend.policy.UnavailablePrograms[id]; reason != "" {
		return "", capabilityUnavailableError{reason: reason}
	}
	if !s.programAllowed(id) {
		return "", sandbox.ErrDenied
	}
	if !s.nativeFull {
		return s.backend.policy.Programs[id].Executable, nil
	}
	return "/almagest/program/" + string(id), nil
}

func (s *instance) ProcessContext() sandbox.ProcessContext {
	if s.nativeFull {
		return sandbox.ProcessContext{Platform: runtime.GOOS, ControllerPID: 1}
	}
	return sandbox.ProcessContext{Platform: runtime.GOOS, ControllerPID: os.Getpid()}
}

func (s *instance) NewCommand(id sandbox.ProgramID, launch sandbox.Launch) (sandbox.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != sandbox.Ready {
		return nil, sandbox.ErrClosed
	}
	if len(s.commands)+len(s.pending) >= s.template.Limits.Commands {
		return nil, sandbox.ErrQuota
	}
	program, ok := s.backend.policy.Programs[id]
	if !ok || !s.programAllowed(id) {
		return nil, sandbox.ErrDenied
	}
	arguments, directory, err := s.buildLaunch(program, launch)
	if err != nil {
		return nil, err
	}
	s.nextCommand++
	cmd := newCommand(s, id, program.Executable, arguments, directory, launch.Directory, s.nextCommand)
	s.pending[cmd] = struct{}{}
	return cmd, nil
}

func (s *instance) programAllowed(id sandbox.ProgramID) bool {
	return slices.Contains(s.template.Programs, id)
}

func (s *instance) buildLaunch(program policy.Program, launch sandbox.Launch) ([]string, string, error) {
	if err := sandbox.ValidatePath(launch.Directory, true); err != nil {
		return nil, "", err
	}
	directory, err := s.hostPath(launch.Directory)
	if err != nil {
		return nil, "", err
	}
	for name := range launch.Inputs {
		if _, ok := program.Inputs[name]; !ok {
			return nil, "", fmt.Errorf("%w: unknown input %q", sandbox.ErrInvalid, name)
		}
	}
	for name, spec := range program.Inputs {
		value, ok := launch.Inputs[name]
		if !ok {
			if spec.Required {
				return nil, "", fmt.Errorf("%w: missing input %q", sandbox.ErrInvalid, name)
			}
			continue
		}
		if value.Kind != spec.Kind || value.Validate() != nil {
			return nil, "", fmt.Errorf("%w: input %q", sandbox.ErrInvalid, name)
		}
		if spec.Maximum > 0 && inputLength(value) > spec.Maximum {
			return nil, "", sandbox.ErrQuota
		}
	}
	arguments := make([]string, 0, len(program.Arguments))
	for _, item := range program.Arguments {
		if item.Input == "" {
			arguments = append(arguments, item.Literal)
			continue
		}
		value := launch.Inputs[item.Input]
		argument, err := s.argumentLocked(value)
		if err != nil {
			return nil, "", err
		}
		arguments = append(arguments, argument)
	}
	return arguments, directory, nil
}

func inputLength(value sandbox.Value) int64 {
	switch value.Kind {
	case sandbox.TextValue:
		return int64(len(value.Text))
	case sandbox.PathValue:
		return int64(len(value.Path.Rel))
	case sandbox.ProgramValue:
		return int64(len(value.Program))
	case sandbox.EndpointValue:
		return int64(len(value.Endpoint))
	default:
		return 0
	}
}

func (s *instance) argumentLocked(value sandbox.Value) (string, error) {
	switch value.Kind {
	case sandbox.TextValue:
		return value.Text, nil
	case sandbox.IntegerValue:
		return fmt.Sprintf("%d", value.Int), nil
	case sandbox.BooleanValue:
		if value.Bool {
			return "true", nil
		}
		return "false", nil
	case sandbox.PathValue:
		if s.nativeFull {
			return s.childPathLocked(value.Path)
		}
		return s.hostPath(value.Path)
	case sandbox.ProgramValue:
		if !s.programAllowed(value.Program) {
			return "", sandbox.ErrDenied
		}
		if s.nativeFull {
			return s.ProgramPath(value.Program)
		}
		return s.backend.policy.Programs[value.Program].Executable, nil
	case sandbox.EndpointValue:
		if err := sandbox.ValidateEndpoint("tcp", value.Endpoint, false); err != nil {
			return "", err
		}
		return value.Endpoint, nil
	default:
		return "", sandbox.ErrInvalid
	}
}

func (s *instance) hostPath(path sandbox.Path) (string, error) {
	if err := sandbox.ValidatePath(path, true); err != nil {
		return "", err
	}
	root := s.temporaryPath
	if path.Root == sandbox.Persistent {
		root = s.persistentPath
	}
	if root == "" {
		return "", sandbox.ErrUnavailable
	}
	if path.Rel == "" || path.Rel == "." {
		return root, nil
	}
	return filepath.Join(root, filepath.FromSlash(path.Rel)), nil
}

func (s *instance) Done() <-chan struct{} { return s.done }

func (s *instance) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *instance) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.status.State == sandbox.Ready {
			s.status.State = sandbox.Closing
		}
		s.cancel(sandbox.ErrClosed)
		commands := slices.Collect(func(yield func(*command) bool) {
			for command := range s.commands {
				if !yield(command) {
					return
				}
			}
		})
		pending := slices.Collect(func(yield func(*command) bool) {
			for command := range s.pending {
				if !yield(command) {
					return
				}
			}
		})
		sockets := slices.Collect(func(yield func(func() error) bool) {
			for _, close := range s.sockets {
				if !yield(close) {
					return
				}
			}
		})
		files := slices.Collect(func(yield func(*trackedFile) bool) {
			for file := range s.files {
				if !yield(file) {
					return
				}
			}
		})
		s.mu.Unlock()
		s.mu.Lock()
		result := s.err
		s.mu.Unlock()
		for _, command := range pending {
			result = errors.Join(result, command.closePending())
		}
		for _, close := range sockets {
			result = errors.Join(result, close())
		}
		for _, file := range files {
			result = errors.Join(result, file.Close())
		}
		for _, command := range commands {
			result = errors.Join(result, command.Kill())
			_, waitErr := command.Wait()
			var exitErr *sandbox.ExitError
			expectedKill := errors.As(waitErr, &exitErr) && exitErr.Exit.Killed
			if waitErr != nil && !expectedKill {
				result = errors.Join(result, waitErr)
			}
		}
		result = errors.Join(result, s.closeWindowsBoundary())
		for _, root := range s.roots {
			result = errors.Join(result, root.Close())
		}
		result = errors.Join(result, s.closeNativeWorker())
		result = errors.Join(result, removeVerifiedChild(s.backend.policy.TemporaryRoot, s.temporaryName, s.temporaryInfo))
		s.mu.Lock()
		s.err = result
		if result != nil {
			s.status.State = sandbox.Failed
			s.status.Cleanup = "failed"
		} else {
			s.status.State = sandbox.Closed
			s.status.Cleanup = "complete"
		}
		close(s.done)
		s.mu.Unlock()
	})
	<-s.done
	return s.Err()
}

func persistentRootClose(root *confinedRoot) error {
	if root == nil {
		return nil
	}
	return root.Close()
}

func removeVerifiedChild(parentPath, name string, expected os.FileInfo) error {
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	current, err := parent.Lstat(name)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return errors.Join(fmt.Errorf("%w: temporary storage identity changed", sandbox.ErrUnavailable), err)
	}
	return parent.RemoveAll(name)
}

func (s *instance) forgetCommand(command *command) {
	s.mu.Lock()
	_, active := s.commands[command]
	if active {
		delete(s.commands, command)
	}
	s.mu.Unlock()
	if active {
		s.backend.releaseCommand()
	}
}

func (s *instance) forgetPendingCommand(command *command) {
	s.mu.Lock()
	delete(s.pending, command)
	s.mu.Unlock()
}

func (s *instance) reserveCommand(command *command) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != sandbox.Ready {
		return sandbox.ErrClosed
	}
	if len(s.commands) >= s.template.Limits.Commands {
		return sandbox.ErrQuota
	}
	if !s.backend.reserveCommand() {
		return sandbox.ErrQuota
	}
	if _, ok := s.pending[command]; !ok {
		s.backend.releaseCommand()
		return sandbox.ErrClosed
	}
	delete(s.pending, command)
	s.commands[command] = struct{}{}
	return nil
}

func (b *Backend) reserveCommand() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.activeCommands >= b.policy.Limits.Commands {
		return false
	}
	b.activeCommands++
	return true
}

func (b *Backend) releaseCommand() {
	b.mu.Lock()
	b.activeCommands--
	b.mu.Unlock()
}

func (s *instance) trackSocket(value any, close func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != sandbox.Ready {
		return sandbox.ErrClosed
	}
	if len(s.sockets) >= s.template.Limits.Sockets {
		return sandbox.ErrQuota
	}
	s.sockets[value] = close
	return nil
}

func (s *instance) forgetSocket(value any) {
	s.mu.Lock()
	delete(s.sockets, value)
	s.mu.Unlock()
}

func (s *instance) trackFile(file *trackedFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != sandbox.Ready {
		return sandbox.ErrClosed
	}
	s.files[file] = struct{}{}
	return nil
}

func (s *instance) forgetFile(file *trackedFile) {
	s.mu.Lock()
	delete(s.files, file)
	s.mu.Unlock()
}

func (s *instance) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := sandbox.ValidateEndpoint(network, address, false); err != nil {
		return nil, err
	}
	operation, cancel := s.socketContext(ctx)
	defer cancel()
	var conn net.Conn
	var err error
	if s.nativeFull {
		file, openErr := s.nativeSocket(operation, "dial", network, address)
		if openErr != nil {
			return nil, openErr
		}
		conn, err = net.FileConn(file)
		err = errors.Join(err, file.Close())
	} else {
		conn, err = (&net.Dialer{}).DialContext(operation, network, address)
	}
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, owner: s}
	if err := s.trackSocket(tracked, tracked.Close); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tracked, nil
}

func (s *instance) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	if err := sandbox.ValidateEndpoint(network, address, true); err != nil {
		return nil, err
	}
	operation, cancel := s.socketContext(ctx)
	defer cancel()
	var listener net.Listener
	var err error
	if s.nativeFull {
		file, openErr := s.nativeSocket(operation, "listen", network, address)
		if openErr != nil {
			return nil, openErr
		}
		listener, err = net.FileListener(file)
		err = errors.Join(err, file.Close())
	} else {
		listener, err = (&net.ListenConfig{}).Listen(operation, network, address)
	}
	if err != nil {
		return nil, err
	}
	tracked := &trackedListener{Listener: listener, owner: s}
	if err := s.trackSocket(tracked, tracked.Close); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return tracked, nil
}

func (s *instance) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	if err := sandbox.ValidateEndpoint(network, address, true); err != nil {
		return nil, err
	}
	operation, cancel := s.socketContext(ctx)
	defer cancel()
	var packet net.PacketConn
	var err error
	if s.nativeFull {
		file, openErr := s.nativeSocket(operation, "packet", network, address)
		if openErr != nil {
			return nil, openErr
		}
		packet, err = net.FilePacketConn(file)
		err = errors.Join(err, file.Close())
	} else {
		packet, err = (&net.ListenConfig{}).ListenPacket(operation, network, address)
	}
	if err != nil {
		return nil, err
	}
	tracked := &trackedPacketConn{PacketConn: packet, owner: s}
	if err := s.trackSocket(tracked, tracked.Close); err != nil {
		_ = packet.Close()
		return nil, err
	}
	return tracked, nil
}

func (s *instance) DialUnix(ctx context.Context, socketPath sandbox.Path) (net.Conn, error) {
	if !supportsUnixSockets() && !s.nativeFull {
		return nil, sandbox.ErrUnsupported
	}
	operation, cancel := s.socketContext(ctx)
	defer cancel()
	root, hostPath, err := s.validateUnixSocketPath(operation, socketPath, true)
	if err != nil {
		return nil, err
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.closed {
		return nil, sandbox.ErrClosed
	}
	info, err := root.root.Lstat(socketPath.Rel)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(sandbox.ErrDenied, err)
	}
	var conn net.Conn
	if s.nativeFull {
		childPath, childErr := s.ChildPath(socketPath)
		if childErr != nil {
			return nil, childErr
		}
		file, openErr := s.nativeSocket(operation, "dial", "unix", childPath)
		if openErr != nil {
			return nil, openErr
		}
		conn, err = net.FileConn(file)
		err = errors.Join(err, file.Close())
	} else {
		conn, err = (&net.Dialer{}).DialContext(operation, "unix", hostPath)
	}
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, owner: s}
	if err := s.trackSocket(tracked, tracked.Close); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tracked, nil
}

func (s *instance) ListenUnix(ctx context.Context, socketPath sandbox.Path) (net.Listener, error) {
	if !supportsUnixSockets() && !s.nativeFull {
		return nil, sandbox.ErrUnsupported
	}
	operation, cancel := s.socketContext(ctx)
	defer cancel()
	root, hostPath, err := s.validateUnixSocketPath(operation, socketPath, false)
	if err != nil {
		return nil, err
	}
	root.mu.Lock()
	if root.closed {
		root.mu.Unlock()
		return nil, sandbox.ErrClosed
	}
	if _, err = root.root.Lstat(socketPath.Rel); !errors.Is(err, os.ErrNotExist) {
		root.mu.Unlock()
		if err == nil {
			return nil, sandbox.ErrConflict
		}
		return nil, err
	}
	var listener net.Listener
	if s.nativeFull {
		childPath, childErr := s.ChildPath(socketPath)
		if childErr != nil {
			root.mu.Unlock()
			return nil, childErr
		}
		file, openErr := s.nativeSocket(operation, "listen", "unix", childPath)
		if openErr != nil {
			root.mu.Unlock()
			return nil, openErr
		}
		listener, err = net.FileListener(file)
		err = errors.Join(err, file.Close())
	} else {
		listener, err = (&net.ListenConfig{}).Listen(operation, "unix", hostPath)
	}
	if err != nil {
		root.mu.Unlock()
		return nil, err
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		root.mu.Unlock()
		_ = listener.Close()
		return nil, sandbox.ErrUnsupported
	}
	unixListener.SetUnlinkOnClose(false)
	identity, statErr := root.root.Lstat(socketPath.Rel)
	if statErr != nil || identity.Mode()&os.ModeSocket == 0 || identity.Mode()&os.ModeSymlink != 0 {
		root.mu.Unlock()
		_ = listener.Close()
		return nil, errors.Join(sandbox.ErrDenied, statErr)
	}
	root.mu.Unlock()
	tracked := &trackedListener{Listener: listener, owner: s, cleanup: func() error {
		root.mu.Lock()
		defer root.mu.Unlock()
		if root.closed {
			return sandbox.ErrClosed
		}
		current, statErr := root.root.Lstat(socketPath.Rel)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil || !os.SameFile(identity, current) {
			return errors.Join(sandbox.ErrUnavailable, statErr)
		}
		return root.root.Remove(socketPath.Rel)
	}}
	if err := s.trackSocket(tracked, tracked.Close); err != nil {
		_ = tracked.Close()
		return nil, err
	}
	return tracked, nil
}

func (s *instance) socketContext(ctx context.Context) (context.Context, context.CancelFunc) {
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return operation, func() {
		stop()
		cancel()
	}
}

func (s *instance) validateUnixSocketPath(ctx context.Context, socketPath sandbox.Path, mustExist bool) (*confinedRoot, string, error) {
	if err := sandbox.ValidatePath(socketPath, false); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	if s.status.State != sandbox.Ready {
		s.mu.Unlock()
		return nil, "", sandbox.ErrClosed
	}
	root := s.roots[socketPath.Root]
	s.mu.Unlock()
	if root == nil {
		return nil, "", sandbox.ErrUnavailable
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.closed {
		return nil, "", sandbox.ErrClosed
	}
	parent := path.Dir(socketPath.Rel)
	if _, err := root.validate(parent, true); err != nil {
		return nil, "", err
	}
	if mustExist {
		if _, err := root.root.Lstat(socketPath.Rel); err != nil {
			return nil, "", err
		}
	}
	hostPath, err := s.hostPath(socketPath)
	return root, hostPath, err
}

type trackedConn struct {
	net.Conn
	owner *instance
	once  sync.Once
}

func (c *trackedConn) closeNative() error { return c.Conn.Close() }
func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() { err = c.closeNative(); c.owner.forgetSocket(c) })
	return err
}

type trackedListener struct {
	net.Listener
	owner   *instance
	cleanup func() error
	once    sync.Once
}

func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, owner: l.owner}
	if err := l.owner.trackSocket(tracked, tracked.Close); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tracked, nil
}

func (l *trackedListener) closeNative() error {
	result := l.Listener.Close()
	if l.cleanup != nil {
		result = errors.Join(result, l.cleanup())
	}
	return result
}
func (l *trackedListener) Close() error {
	var err error
	l.once.Do(func() { err = l.closeNative(); l.owner.forgetSocket(l) })
	return err
}

type trackedPacketConn struct {
	net.PacketConn
	owner *instance
	once  sync.Once
}

func (c *trackedPacketConn) Read(data []byte) (int, error) {
	connection, ok := c.PacketConn.(net.Conn)
	if !ok {
		return 0, sandbox.ErrUnsupported
	}
	return connection.Read(data)
}

func (c *trackedPacketConn) Write(data []byte) (int, error) {
	connection, ok := c.PacketConn.(net.Conn)
	if !ok {
		return 0, sandbox.ErrUnsupported
	}
	return connection.Write(data)
}

func (c *trackedPacketConn) RemoteAddr() net.Addr {
	connection, ok := c.PacketConn.(net.Conn)
	if !ok {
		return nil
	}
	return connection.RemoteAddr()
}

func (c *trackedPacketConn) WriteTo(data []byte, address net.Addr) (int, error) {
	if address == nil {
		return 0, sandbox.ErrInvalid
	}
	network := address.Network()
	if network != "udp" && network != "udp4" && network != "udp6" {
		return 0, sandbox.ErrUnsupported
	}
	if err := sandbox.ValidateEndpoint(network, address.String(), false); err != nil {
		return 0, err
	}
	return c.PacketConn.WriteTo(data, address)
}

func (c *trackedPacketConn) closeNative() error { return c.PacketConn.Close() }
func (c *trackedPacketConn) Close() error {
	var err error
	c.once.Do(func() { err = c.closeNative(); c.owner.forgetSocket(c) })
	return err
}
