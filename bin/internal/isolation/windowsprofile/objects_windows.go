//go:build windows

package windowsprofile

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	userenv            = windows.NewLazySystemDLL("userenv.dll")
	createAppContainer = userenv.NewProc("CreateAppContainerProfile")
	deleteAppContainer = userenv.NewProc("DeleteAppContainerProfile")
)

// Identity is a distinct AppContainer created by trusted bootstrap. Keep it
// alive until the role job is closed. Network uses the caller token instead.
type Identity struct {
	role Role
	name *uint16
	sid  *windows.SID
}

func NewIdentity(role Role, instance string) (*Identity, error) {
	if err := CheckPlatform(windows.RtlGetVersion().BuildNumber, runtime.GOARCH); err != nil {
		return nil, err
	}
	if role == Network || DefaultPlan(role).Validate() != nil || len(instance) < 1 || len(instance) > 64 {
		return nil, fmt.Errorf("%w: invalid AppContainer identity", ErrUnavailable)
	}
	for _, c := range instance {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return nil, fmt.Errorf("%w: invalid instance name", ErrUnavailable)
		}
	}
	name, _ := windows.UTF16PtrFromString("Almagest." + string(role) + "." + instance)
	var sid *windows.SID
	result, _, _ := createAppContainer.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if result != 0 {
		return nil, fmt.Errorf("%w: create distinct AppContainer: HRESULT %#x", ErrUnavailable, result)
	}
	return &Identity{role: role, name: name, sid: sid}, nil
}

func (i *Identity) Close() error {
	if i == nil || i.sid == nil {
		return nil
	}
	result, _, _ := deleteAppContainer.Call(uintptr(unsafe.Pointer(i.name)))
	if result != 0 {
		return fmt.Errorf("delete AppContainer: HRESULT %#x", result)
	}
	err := windows.FreeSid(i.sid)
	i.sid = nil
	return err
}

func trustedSID(sid, owner *windows.SID) bool {
	// The volume root can be owned by the Windows Modules Installer service.
	// This is its fixed service SID, not an arbitrary service or package SID.
	return sid != nil && (sid.Equals(owner) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) || sid.String() == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464")
}

// lockPath holds every ancestor without write/delete sharing. It prevents
// replacement between ACL inspection and CreateProcess. The final writable
// object can grant access to this one role SID; parents never can.
func lockPath(path string, directory bool, writableSID *windows.SID) ([]windows.Handle, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	var held []windows.Handle
	fail := func(err error) ([]windows.Handle, error) { closeHandles(held); return nil, err }
	parts := strings.Split(path[3:], `\`)
	current := path[:3]
	// Include the volume root: its DELETE_CHILD right can replace the first parent.
	for index := -1; index < len(parts); index++ {
		if index >= 0 {
			current = filepath.Join(current, parts[index])
		}
		final := index == len(parts)-1
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return fail(err)
		}
		flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT | windows.FILE_FLAG_BACKUP_SEMANTICS)
		access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
		h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
		if err != nil {
			return fail(fmt.Errorf("%w: hold trusted path %s: %v", ErrUnavailable, current, err))
		}
		held = append(held, h)
		var info windows.ByHandleFileInformation
		if err = windows.GetFileInformationByHandle(h, &info); err != nil {
			return fail(err)
		}
		isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || isDir != (!final || directory) || !isDir && info.NumberOfLinks != 1 {
			return fail(fmt.Errorf("%w: reparse point, alias or incorrect object type", ErrUnavailable))
		}
		sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return fail(err)
		}
		owner, _, err := sd.Owner()
		if err != nil || !trustedSID(owner, user.User.Sid) {
			return fail(fmt.Errorf("%w: untrusted owner %v at %s", ErrUnavailable, owner, current))
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil {
			return fail(fmt.Errorf("%w: missing bounded DACL", ErrUnavailable))
		}
		roleAccess := false
		for n := uint16(0); n < acl.AceCount; n++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, uint32(n), &ace); err != nil {
				return fail(err)
			}
			if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
				continue
			}
			if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
				continue
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
				return fail(fmt.Errorf("%w: unsupported ACL entry", ErrUnavailable))
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if final && writableSID != nil && sid.Equals(writableSID) {
				// Require data access, but never permission to change the boundary.
				if uint32(ace.Mask)&uint32(windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_ALL) != 0 {
					return fail(fmt.Errorf("%w: role can change its storage ACL", ErrUnavailable))
				}
				roleAccess = uint32(ace.Mask)&(windows.FILE_READ_DATA|windows.FILE_WRITE_DATA) == windows.FILE_READ_DATA|windows.FILE_WRITE_DATA
			}
			if trustedSID(sid, user.User.Sid) || final && writableSID != nil && sid.Equals(writableSID) {
				continue
			}
			mask := uint32(windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL | 0x40)
			if final || !directory && index == len(parts)-2 {
				mask |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.GENERIC_WRITE
			}
			if uint32(ace.Mask)&mask != 0 || final && writableSID != nil {
				return fail(fmt.Errorf("%w: untrusted path mutation rights", ErrUnavailable))
			}
		}
		if final && writableSID != nil {
			if !roleAccess {
				return fail(fmt.Errorf("%w: storage has no exact role grant", ErrUnavailable))
			}
			if err := checkLowLabel(h); err != nil {
				return fail(err)
			}
		}
	}
	return held, nil
}

func checkLowLabel(h windows.Handle) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	sacl, _, err := sd.SACL()
	if err != nil || sacl == nil {
		return fmt.Errorf("%w: missing storage integrity label", ErrUnavailable)
	}
	for n := uint16(0); n < sacl.AceCount; n++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(sacl, uint32(n), &ace); err != nil {
			return err
		}
		if ace.Header.AceType != 0x11 {
			continue
		} // SYSTEM_MANDATORY_LABEL_ACE_TYPE
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.String() == "S-1-16-4096" && uint32(ace.Mask)&1 != 0 && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: storage must have Low integrity and no-write-up", ErrUnavailable)
}

func closeHandles(handles []windows.Handle) {
	for n := len(handles) - 1; n >= 0; n-- {
		_ = windows.CloseHandle(handles[n])
	}
}

// ProvisionDirectory creates a NEW dedicated directory, with a protected DACL
// and Low mandatory label. It never edits an existing tree or follows links.
// Use separate identities/directories for R, C and H. C receives its dedicated
// config directory; it receives no access to R's private application stores.
func (i *Identity) ProvisionDirectory(path string) error {
	if i == nil || i.sid == nil {
		return fmt.Errorf("%w: closed identity", ErrUnavailable)
	}
	if err := validatePath(path); err != nil {
		return err
	}
	parents, err := lockPath(filepath.Dir(path), true, nil)
	if err != nil {
		return err
	}
	defer closeHandles(parents)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;S-1-5-32-544)(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;0x1301bf;;;" + i.sid.String() + ")S:(ML;OICI;NW;;;LW)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, _ := windows.UTF16PtrFromString(path)
	return windows.CreateDirectory(name, &sa)
}

// GrantExecutable is a provisioning operation on a trusted, single-link
// executable. It grants this identity read/execute only. The role cannot alter
// the binary or its parent. Deployment owns removal of obsolete identity ACEs.
func (i *Identity) GrantExecutable(path string) error {
	if i == nil || i.sid == nil {
		return fmt.Errorf("%w: closed identity", ErrUnavailable)
	}
	held, err := lockPath(path, false, nil)
	if err != nil {
		return err
	}
	defer closeHandles(held)
	name, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	entry := windows.EXPLICIT_ACCESS{AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE, AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_GROUP, TrusteeValue: windows.TrusteeValueFromSID(i.sid)}}
	acl, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, acl)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
