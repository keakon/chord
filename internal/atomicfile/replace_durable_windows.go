package atomicfile

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateSecurityAttributes() (*windows.SecurityAttributes, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

func makeDurableDirectory(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := makeDurableDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	sa, err := privateSecurityAttributes()
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(p, sa); err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return err
	}
	return nil
}

func createDurableTemp(dir, base string, _ os.FileMode) (*os.File, error) {
	sa, err := privateSecurityAttributes()
	if err != nil {
		return nil, err
	}
	for range 16 {
		path := filepath.Join(dir, "."+base+"."+rand.Text()+".tmp")
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == windows.ERROR_FILE_EXISTS || err == windows.ERROR_ALREADY_EXISTS {
			continue
		}
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(h), path), nil
	}
	return nil, fmt.Errorf("temporary file name attempts exhausted")
}

func commitDurable(source, target string) (ReplaceOutcome, error) {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return NotReplaced, err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return NotReplaced, err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return ReplacementUnknown, fmt.Errorf("replace file with write through: %w", err)
	}
	return Durable, nil
}
