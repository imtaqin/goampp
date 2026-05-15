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
// Strategy (in order):
//  1. If the current token is elevated AND has a linked standard-user token
//     (i.e. UAC is on), return that linked token — it's already non-admin.
//  2. Otherwise call CreateRestrictedToken with the Administrators SID in
//     SidsToDisable so the deny-only flag is set.
//
// The caller must close the returned token.
func postgresToken() (windows.Token, error) {
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

	// Prefer the UAC-paired standard-user token — it's already non-admin.
	if linked, err := cur.GetLinkedToken(); err == nil {
		return linked, nil
	}

	// Fallback: duplicate + disable the Administrators SID.
	return disableAdminSID(cur)
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
