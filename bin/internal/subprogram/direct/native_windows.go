//go:build windows

package direct

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
	"golang.org/x/sys/windows"
)

const qualifiedWindowsBuild = 20348

const (
	createRestrictedTokenDisableMaxPrivilege = 0x1
	createRestrictedTokenWriteRestricted     = 0x8
)

var createSandboxRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
var windowsUser32 = windows.NewLazySystemDLL("user32.dll")
var getProcessWindowStation = windowsUser32.NewProc("GetProcessWindowStation")
var getThreadDesktop = windowsUser32.NewProc("GetThreadDesktop")

// Executable and USER-object ACLs can be shared by separate backend instances.
// Serialize each read-modify-write update for the complete process, not only
// for one backend.
var windowsBoundaryACL sync.Mutex

type windowsInstanceBoundary struct {
	token       windows.Token
	sid         *windows.SID
	job         windows.Handle
	granted     []*os.File
	storage     []*os.File
	userObjects []windows.Handle
}

func windowsNativePrerequisites(template policy.Template) error {
	if runtime.GOARCH != "amd64" || windows.RtlGetVersion().BuildNumber != qualifiedWindowsBuild {
		return fmt.Errorf("%w: windows-native-v1 is qualified only for Windows build %d on amd64", sandbox.ErrUnavailable, qualifiedWindowsBuild)
	}
	for _, required := range template.Limits.Required {
		if required == "Files" || required == "TemporaryBytes" {
			return fmt.Errorf("%w: Windows cannot enforce required limit %s", sandbox.ErrUnavailable, required)
		}
	}
	return nil
}

func (s *instance) startWindowsBoundary() error {
	if err := windowsNativePrerequisites(s.template); err != nil {
		return err
	}
	sid, err := sandboxSID(s.owner, s.slot, s.templateID)
	if err != nil {
		return fmt.Errorf("%w: create Windows sandbox SID: %v", sandbox.ErrUnavailable, err)
	}
	token, err := restrictedSandboxToken(sid)
	if err != nil {
		return fmt.Errorf("%w: create Windows sandbox token: %v", sandbox.ErrUnavailable, err)
	}
	fail := func(cause error) error {
		_ = token.Close()
		return cause
	}
	if err = verifySandboxToken(token, sid); err != nil {
		return fail(fmt.Errorf("%w: verify Windows sandbox token: %v", sandbox.ErrUnavailable, err))
	}
	var storage []*os.File
	for index, name := range []string{s.temporaryPath, s.persistentPath} {
		if name == "" {
			continue
		}
		expected := []os.FileInfo{s.temporaryInfo, s.persistentInfo}[index]
		held, protectErr := protectSandboxDirectory(name, expected, sid)
		if protectErr != nil {
			for _, file := range storage {
				_ = file.Close()
			}
			return fail(fmt.Errorf("%w: protect Windows sandbox storage: %v", sandbox.ErrUnavailable, protectErr))
		}
		storage = append(storage, held)
	}
	var granted []*os.File
	windowsBoundaryACL.Lock()
	userObjects, err := grantWindowsUserObjects(sid)
	if err != nil {
		for _, file := range storage {
			_ = file.Close()
		}
		windowsBoundaryACL.Unlock()
		return fail(fmt.Errorf("%w: grant Windows desktop access: %v", sandbox.ErrUnavailable, err))
	}
	for _, id := range s.template.Programs {
		handle, grantErr := grantWindowsExecutable(s.backend.policy.Programs[id].Executable, s.backend.programInfo[id], sid)
		if grantErr != nil {
			for _, grantedHandle := range granted {
				_ = revokeWindowsGrant(grantedHandle, sid)
			}
			for _, object := range userObjects {
				_ = updateWindowsObjectGrant(object, sid, 0, windows.REVOKE_ACCESS)
			}
			for _, file := range storage {
				_ = file.Close()
			}
			windowsBoundaryACL.Unlock()
			return fail(fmt.Errorf("%w: grant Windows sandbox executable: %v", sandbox.ErrUnavailable, grantErr))
		}
		granted = append(granted, handle)
	}
	windowsBoundaryACL.Unlock()
	job, err := newWindowsInstanceJob(s.template.Limits)
	if err != nil {
		windowsBoundaryACL.Lock()
		for _, handle := range granted {
			_ = revokeWindowsGrant(handle, sid)
		}
		for _, object := range userObjects {
			_ = updateWindowsObjectGrant(object, sid, 0, windows.REVOKE_ACCESS)
		}
		windowsBoundaryACL.Unlock()
		for _, file := range storage {
			_ = file.Close()
		}
		return fail(fmt.Errorf("%w: create Windows sandbox job: %v", sandbox.ErrUnavailable, err))
	}
	s.windowsBoundary = &windowsInstanceBoundary{token: token, sid: sid, job: job, granted: granted, storage: storage, userObjects: userObjects}
	s.status.EffectiveIdentity = "restricted-low:" + sid.String()
	return nil
}

func (s *instance) closeWindowsBoundary() error {
	if s.windowsBoundary == nil {
		return nil
	}
	b := s.windowsBoundary
	s.windowsBoundary = nil
	err := terminateAndWaitJob(b.job)
	err = errors.Join(err, windows.CloseHandle(b.job))
	windowsBoundaryACL.Lock()
	for _, handle := range b.granted {
		err = errors.Join(err, revokeWindowsGrant(handle, b.sid))
	}
	for _, object := range b.userObjects {
		err = errors.Join(err, updateWindowsObjectGrant(object, b.sid, 0, windows.REVOKE_ACCESS))
	}
	windowsBoundaryACL.Unlock()
	for _, file := range b.storage {
		err = errors.Join(err, file.Close())
	}
	err = errors.Join(err, b.token.Close())
	return err
}

func sandboxSID(owner sandbox.Owner, slot string, template sandbox.TemplateID) (*windows.SID, error) {
	hash := sha256.Sum256([]byte(owner.Workspace + "\x00" + owner.Key + "\x00" + slot + "\x00" + string(template)))
	parts := make([]string, 4)
	for index := range parts {
		value := binary.LittleEndian.Uint32(hash[index*4:])
		if value == 0 {
			value = uint32(index + 1)
		}
		parts[index] = fmt.Sprintf("%d", value)
	}
	return windows.StringToSid("S-1-5-21-" + strings.Join(parts, "-"))
}

func restrictedSandboxToken(identity *windows.SID) (windows.Token, error) {
	var source windows.Token
	access := uint32(windows.TOKEN_QUERY | windows.TOKEN_DUPLICATE | windows.TOKEN_ASSIGN_PRIMARY | windows.TOKEN_ADJUST_DEFAULT)
	if err := windows.OpenProcessToken(windows.CurrentProcess(), access, &source); err != nil {
		return 0, err
	}
	defer func() { _ = source.Close() }()
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return 0, err
	}
	disabled := []windows.SIDAndAttributes{{Sid: admin}}
	// Apply the restricting-SID access check only to writes. Windows system
	// services such as CryptoAPI must remain readable through the filtered
	// source identity, while writes require the stable instance SID and are
	// therefore limited to roots that this boundary explicitly grants.
	restricted := []windows.SIDAndAttributes{{Sid: identity}}
	var token windows.Token
	flags := uintptr(createRestrictedTokenDisableMaxPrivilege | createRestrictedTokenWriteRestricted)
	result, _, callErr := createSandboxRestrictedToken.Call(uintptr(source), flags,
		uintptr(len(disabled)), uintptr(unsafe.Pointer(&disabled[0])), 0, 0,
		uintptr(len(restricted)), uintptr(unsafe.Pointer(&restricted[0])), uintptr(unsafe.Pointer(&token)))
	if result == 0 {
		return 0, callErr
	}
	low, err := windows.StringToSid("S-1-16-4096")
	if err != nil {
		_ = token.Close()
		return 0, err
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: low, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err = windows.SetTokenInformation(token, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size()); err != nil {
		_ = token.Close()
		return 0, err
	}
	return token, nil
}

func verifySandboxToken(token windows.Token, identity *windows.SID) error {
	if token.IsElevated() {
		return errors.New("token remains elevated")
	}
	if restricted, err := token.IsRestricted(); err != nil || !restricted {
		return fmt.Errorf("token has no restricting SID: %v", err)
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return err
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			return errors.New("administrators group remains enabled")
		}
	}
	data, err := boundedTokenInformation(token, windows.TokenRestrictedSids)
	if err != nil {
		return err
	}
	restrictedGroups := (*windows.Tokengroups)(unsafe.Pointer(&data[0]))
	if !slices.ContainsFunc(restrictedGroups.AllGroups(), func(group windows.SIDAndAttributes) bool { return group.Sid.Equals(identity) }) {
		return errors.New("instance restricting SID is absent")
	}
	data, err = boundedTokenInformation(token, windows.TokenIntegrityLevel)
	if err != nil {
		return err
	}
	mandatory := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&data[0]))
	count := mandatory.Label.Sid.SubAuthorityCount()
	if count == 0 || mandatory.Label.Sid.SubAuthority(uint32(count-1)) != 0x1000 {
		return errors.New("token integrity is not Low")
	}
	data, err = boundedTokenInformation(token, windows.TokenPrivileges)
	if err != nil {
		return err
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&data[0]))
	for _, privilege := range privileges.AllPrivileges() {
		if privilege.Luid.HighPart != 0 || privilege.Luid.LowPart != 23 {
			return errors.New("unexpected token privilege remains")
		}
	}
	return nil
}

func boundedTokenInformation(token windows.Token, class uint32) ([]byte, error) {
	data := make([]byte, 256)
	var size uint32
	err := windows.GetTokenInformation(token, class, &data[0], uint32(len(data)), &size)
	if (errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || errors.Is(err, windows.ERROR_BAD_LENGTH)) && size > uint32(len(data)) && size <= 64<<10 {
		data = make([]byte, size)
		err = windows.GetTokenInformation(token, class, &data[0], size, &size)
	}
	return data, err
}

func protectSandboxDirectory(name string, expected os.FileInfo, identity *windows.SID) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	fail := func(cause error) (*os.File, error) { _ = file.Close(); return nil, cause }
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fail(err)
	}
	held, err := file.Stat()
	if err != nil || expected == nil || !os.SameFile(expected, held) ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fail(errors.Join(sandbox.ErrDenied, err))
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fail(err)
	}
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;S-1-5-32-544)(A;OICI;FA;;;" + user.User.Sid.String() + ")" +
		"(A;OICI;0x1301bf;;;" + identity.String() + ")S:(ML;OICI;NW;;;LW)"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fail(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fail(errors.Join(errors.New("missing storage DACL"), err))
	}
	sacl, _, err := descriptor.SACL()
	if err != nil || sacl == nil {
		return fail(errors.Join(errors.New("missing storage mandatory label"), err))
	}
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION,
		nil, nil, dacl, sacl); err != nil {
		return fail(err)
	}
	return file, nil
}

func grantWindowsUserObjects(identity *windows.SID) ([]windows.Handle, error) {
	stationValue, _, stationErr := getProcessWindowStation.Call()
	if stationValue == 0 {
		return nil, stationErr
	}
	desktopValue, _, desktopErr := getThreadDesktop.Call(uintptr(windows.GetCurrentThreadId()))
	if desktopValue == 0 {
		return nil, desktopErr
	}
	station := windows.Handle(stationValue)
	desktop := windows.Handle(desktopValue)
	// These masks are WINSTA_ALL_ACCESS and DESKTOP_ALL_ACCESS. Job UI limits
	// still reject the sensitive operations after the DACL permits USER32 to
	// connect the restricted token to its inherited station and desktop.
	if err := updateWindowsObjectGrant(station, identity, 0x0000037f, windows.GRANT_ACCESS); err != nil {
		return nil, err
	}
	if err := updateWindowsObjectGrant(desktop, identity, 0x000001ff, windows.GRANT_ACCESS); err != nil {
		_ = updateWindowsObjectGrant(station, identity, 0, windows.REVOKE_ACCESS)
		return nil, err
	}
	return []windows.Handle{station, desktop}, nil
}

func updateWindowsObjectGrant(handle windows.Handle, identity *windows.SID, permissions windows.ACCESS_MASK, mode windows.ACCESS_MODE) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return errors.Join(errors.New("missing USER object DACL"), err)
	}
	entry := windows.EXPLICIT_ACCESS{AccessPermissions: permissions, AccessMode: mode,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID,
			TrusteeType: windows.TRUSTEE_IS_GROUP, TrusteeValue: windows.TrusteeValueFromSID(identity)}}
	acl, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, acl)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(handle, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func grantWindowsExecutable(name string, expected os.FileInfo, identity *windows.SID) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	fail := func(cause error) (*os.File, error) { _ = file.Close(); return nil, cause }
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fail(err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || info.NumberOfLinks != 1 {
		return fail(sandbox.ErrDenied)
	}
	held, err := file.Stat()
	if err != nil || expected == nil || !os.SameFile(expected, held) {
		return fail(errors.Join(sandbox.ErrDenied, err))
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fail(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return fail(errors.Join(errors.New("missing executable DACL"), err))
	}
	entry := windows.EXPLICIT_ACCESS{AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE,
		AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID,
			TrusteeType: windows.TRUSTEE_IS_GROUP, TrusteeValue: windows.TrusteeValueFromSID(identity)}}
	acl, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, acl)
	if err != nil {
		return fail(err)
	}
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fail(err)
	}
	return file, nil
}

func revokeWindowsGrant(file *os.File, identity *windows.SID) error {
	handle := windows.Handle(file.Fd())
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err == nil {
		var acl *windows.ACL
		acl, _, err = descriptor.DACL()
		if err == nil && acl != nil {
			entry := windows.EXPLICIT_ACCESS{AccessMode: windows.REVOKE_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID,
				TrusteeType: windows.TRUSTEE_IS_GROUP, TrusteeValue: windows.TrusteeValueFromSID(identity)}}
			acl, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, acl)
			if err == nil {
				err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
			}
		}
	}
	return errors.Join(err, file.Close())
}

func newWindowsInstanceJob(limits policy.TemplateLimits) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	fail := func(cause error) (windows.Handle, error) { _ = windows.CloseHandle(job); return 0, cause }
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_JOB_MEMORY | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	info.BasicLimitInformation.ActiveProcessLimit = uint32(limits.Tasks)
	info.JobMemoryLimit = uintptr(limits.MemoryBytes)
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fail(err)
	}
	cpu := struct{ Flags, Rate uint32 }{Flags: 1 | 4, Rate: uint32(limits.CPUPercent * 100)}
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectCpuRateControlInformation, uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu))); err != nil {
		return fail(err)
	}
	// USER32 initialization needs the inherited window-station and desktop
	// handles. JOB_OBJECT_UILIMIT_HANDLES rejects those handles because their
	// objects are owned outside this job, which prevents console programs such
	// as Tor from starting. The Low integrity token supplies the cross-integrity
	// USER-object boundary; retain every other job UI restriction.
	ui := uint32(windows.JOB_OBJECT_UILIMIT_DESKTOP | windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS |
		windows.JOB_OBJECT_UILIMIT_EXITWINDOWS | windows.JOB_OBJECT_UILIMIT_GLOBALATOMS |
		windows.JOB_OBJECT_UILIMIT_READCLIPBOARD | windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS |
		windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD)
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui))); err != nil {
		return fail(err)
	}
	return job, nil
}
