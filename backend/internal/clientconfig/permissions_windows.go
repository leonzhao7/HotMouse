//go:build windows

package clientconfig

import "golang.org/x/sys/windows"

func privatePermissions(path string, dir bool) error {
	token, e := windows.OpenCurrentProcessToken()
	if e != nil {
		return e
	}
	defer token.Close()
	user, e := token.GetTokenUser()
	if e != nil {
		return e
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	descriptor, e := windows.SecurityDescriptorFromString("D:P(A;" + inherit + ";FA;;;" + user.User.Sid.String() + ")")
	if e != nil {
		return e
	}
	acl, _, e := descriptor.DACL()
	if e != nil {
		return e
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
