//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
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

var procCreateProcessWithTokenW = advapi32.NewProc("CreateProcessWithTokenW")

// launchPostgresWithToken launches postgres.exe with a restricted token where
// the Administrators SID is deny-only, using CreateProcessWithTokenW.
// This API only requires SeImpersonatePrivilege (always enabled in elevated
// processes), unlike CreateProcessAsUser which needs SeAssignPrimaryTokenPrivilege.
func launchPostgresWithToken(exePath string, args []string, workDir string, env []string) (
	pid int, stdout io.ReadCloser, stderr io.ReadCloser, err error,
) {
	tok, err := postgresToken()
	if err != nil {
		return 0, nil, nil, fmt.Errorf("postgresToken: %w", err)
	}
	defer tok.Close()

	// Inheritable security attributes for pipe handles.
	sa := windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(sa))

	var outR, outW windows.Handle
	if err = windows.CreatePipe(&outR, &outW, &sa, 0); err != nil {
		return 0, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Read end must NOT be inherited by the child.
	_ = windows.SetHandleInformation(outR, windows.HANDLE_FLAG_INHERIT, 0)

	var errR, errW windows.Handle
	if err = windows.CreatePipe(&errR, &errW, &sa, 0); err != nil {
		windows.CloseHandle(outR)
		windows.CloseHandle(outW)
		return 0, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	_ = windows.SetHandleInformation(errR, windows.HANDLE_FLAG_INHERIT, 0)

	// Null stdin handle — postgres doesn't need interactive input.
	nullFile, _ := os.Open(os.DevNull)
	var nullH windows.Handle
	if nullFile != nil {
		nullH = windows.Handle(nullFile.Fd())
		defer nullFile.Close()
	}

	// Build quoted command line.
	cmdLine, _ := windows.UTF16PtrFromString(buildCmdLine(exePath, args))

	// Build environment block (null-terminated key=value pairs).
	envBlock := buildEnvBlock(env)

	var workDirPtr *uint16
	if workDir != "" {
		workDirPtr, _ = windows.UTF16PtrFromString(workDir)
	}

	const STARTF_USESTDHANDLES = 0x00000100
	const STARTF_USESHOWWINDOW = 0x00000001
	const CREATE_NO_WINDOW = 0x08000000
	const CREATE_UNICODE_ENVIRONMENT = 0x00000400

	si := windows.StartupInfo{
		Flags:     STARTF_USESTDHANDLES | STARTF_USESHOWWINDOW,
		StdInput:  nullH,
		StdOutput: outW,
		StdErr:    errW,
	}
	si.Cb = uint32(unsafe.Sizeof(si))

	var pi windows.ProcessInformation
	r, _, e := procCreateProcessWithTokenW.Call(
		uintptr(tok),
		0, // dwLogonFlags: 0 = don't load profile (faster)
		0, // lpApplicationName: nil, command comes from lpCommandLine
		uintptr(unsafe.Pointer(cmdLine)),
		CREATE_NO_WINDOW|CREATE_UNICODE_ENVIRONMENT,
		uintptr(unsafe.Pointer(&envBlock[0])),
		uintptr(unsafe.Pointer(workDirPtr)),
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)

	// Close write ends — child owns them now; close regardless of success.
	windows.CloseHandle(outW)
	windows.CloseHandle(errW)

	if r == 0 {
		windows.CloseHandle(outR)
		windows.CloseHandle(errR)
		return 0, nil, nil, fmt.Errorf("CreateProcessWithTokenW: %w", e)
	}
	windows.CloseHandle(pi.Thread) // we don't need the thread handle

	return int(pi.ProcessId),
		os.NewFile(uintptr(outR), "postgres-stdout"),
		os.NewFile(uintptr(errR), "postgres-stderr"),
		nil
}

// buildCmdLine builds a Windows command-line string with proper quoting.
func buildCmdLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteCmdArg(exe))
	for _, a := range args {
		parts = append(parts, quoteCmdArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteCmdArg(s string) string {
	if !strings.ContainsAny(s, ` \t"`) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		if c == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	b.WriteByte('"')
	return b.String()
}

// buildEnvBlock converts a []string of "KEY=VALUE" pairs into the
// double-null-terminated UTF-16 block that CreateProcess* APIs expect.
func buildEnvBlock(env []string) []uint16 {
	var block []uint16
	for _, e := range env {
		block = append(block, utf16.Encode([]rune(e))...)
		block = append(block, 0)
	}
	block = append(block, 0) // final double-null terminator
	return block
}
