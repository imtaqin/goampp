//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// postgresToken returns a process token suitable for launching postgres.exe.
//
// PostgreSQL refuses to start when the launching process has the Administrators
// group enabled in its token (pgwin32_is_admin() → CheckTokenMembership returns
// true). This function produces a token where the Administrators SID is present
// but marked DENY_ONLY — CheckTokenMembership then returns false and postgres
// starts normally.
//
// We use CreateRestrictedToken (not the UAC linked token). The MSDN docs for
// CreateProcessAsUser explicitly exempt "restricted version of the caller's
// primary token" from the SeAssignPrimaryTokenPrivilege requirement, so this
// path works from an elevated process without enabling any extra privileges.
// The linked-token path would need SeAssignPrimaryTokenPrivilege enabled,
// which causes ERROR_PRIVILEGE_NOT_HELD from fork/exec.
//
// The caller must close the returned token.
func postgresToken() (windows.Token, error) {
	// CreateProcessAsUser requires these two privileges ENABLED in the calling
	// process. Both are present-but-disabled in elevated admin tokens.
	// SeAssignPrimaryTokenPrivilege: needed to assign a non-caller primary
	// token even when CreateRestrictedToken "restricted version" exemption
	// applies on some Windows builds but not others.
	// SeIncreaseQuotaPrivilege: always required by CreateProcessAsUser.
	_ = enablePrivilege("SeIncreaseQuotaPrivilege")
	_ = enablePrivilege("SeAssignPrimaryTokenPrivilege")

	var cur windows.Token
	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY,
		&cur,
	)
	if err != nil {
		return 0, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer cur.Close()

	return disableAdminSID(cur)
}

// enablePrivilege enables a named privilege in the current process token.
// Returns an error only for diagnostics — the caller can ignore it if the
// privilege is simply absent (e.g. restricted service account).
func enablePrivilege(name string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY,
		&token,
	); err != nil {
		return err
	}
	defer token.Close()

	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
		return err
	}

	privs := windows.Tokenprivileges{
		PrivilegeCount: 1,
	}
	privs.Privileges[0].Luid = luid
	privs.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED

	return windows.AdjustTokenPrivileges(token, false, &privs, 0, nil, nil)
}

// disableAdminSID duplicates cur and disables the Administrators group SID
// so that CheckTokenMembership on Administrators returns false.
func disableAdminSID(cur windows.Token) (windows.Token, error) {
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return 0, fmt.Errorf("CreateWellKnownSid: %w", err)
	}

	var dup windows.Token
	err = windows.DuplicateTokenEx(
		cur,
		windows.TOKEN_ALL_ACCESS,
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&dup,
	)
	if err != nil {
		return 0, fmt.Errorf("DuplicateTokenEx: %w", err)
	}

	sidAttr := windows.SIDAndAttributes{
		Sid:        adminSID,
		Attributes: 0,
	}
	var restricted windows.Token
	err = createRestrictedToken(
		dup,
		0,
		1, &sidAttr,
		0, nil,
		0, nil,
		&restricted,
	)
	dup.Close()
	if err != nil {
		return 0, fmt.Errorf("CreateRestrictedToken: %w", err)
	}
	return restricted, nil
}

var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")
)

func createRestrictedToken(
	existingToken windows.Token,
	flags uint32,
	disableSIDCount uint32,
	sidsToDisable *windows.SIDAndAttributes,
	deletePrivilegeCount uint32,
	privilegesToDelete *windows.LUIDAndAttributes,
	restrictedSIDCount uint32,
	sidsToRestrict *windows.SIDAndAttributes,
	newToken *windows.Token,
) error {
	r, _, err := procCreateRestrictedToken.Call(
		uintptr(existingToken),
		uintptr(flags),
		uintptr(disableSIDCount),
		uintptr(unsafe.Pointer(sidsToDisable)),
		uintptr(deletePrivilegeCount),
		uintptr(unsafe.Pointer(privilegesToDelete)),
		uintptr(restrictedSIDCount),
		uintptr(unsafe.Pointer(sidsToRestrict)),
		uintptr(unsafe.Pointer(newToken)),
	)
	if r == 0 {
		return err
	}
	return nil
}
