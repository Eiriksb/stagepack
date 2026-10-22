package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"stagepack/internal/brand"
)

var platformRegistry assoc = winRegistry{}

type winRegistry struct{}

// subKey is the part of a plan key below HKCU.
func subKey(k string) (string, error) {
	rest, ok := strings.CutPrefix(k, `HKCU\`)
	if !ok {
		return "", fmt.Errorf("registry key outside HKCU: %s", k)
	}
	return rest, nil
}

func (r winRegistry) register(exe string) error { return r.write(RegistryPlan(exe, exe)) }

func (winRegistry) write(plan []RegValue) error {
	for _, v := range plan {
		sub, err := subKey(v.Key)
		if err != nil {
			return err
		}
		k, _, err := registry.CreateKey(registry.CURRENT_USER, sub, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("create %s: %w", v.Key, err)
		}
		if isDWORD(v.Name) {
			n, perr := strconv.ParseUint(v.Value, 10, 32)
			if perr != nil {
				k.Close()
				return fmt.Errorf("%s\\%s: %w", v.Key, v.Name, perr)
			}
			err = k.SetDWordValue(v.Name, uint32(n))
		} else {
			err = k.SetStringValue(v.Name, v.Value)
		}
		k.Close()
		if err != nil {
			return fmt.Errorf("set %s\\%s: %w", v.Key, v.Name, err)
		}
	}
	return nil
}

func (winRegistry) remove() error {
	var errs []error
	// The extension key may have been taken over by another program since: leave it alone then.
	if sub, _ := subKey(extKey); defaultValue(sub) == brand.ProgID {
		errs = append(errs, deleteTree(sub))
	}
	for _, k := range []string{progKey, uninstallKey} {
		sub, _ := subKey(k)
		errs = append(errs, deleteTree(sub))
	}
	return errors.Join(errs...)
}

func defaultValue(sub string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, sub, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, _ := k.GetStringValue("")
	return s
}

// deleteTree deletes sub below HKCU with everything under it; a missing key is fine.
func deleteTree(sub string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, sub, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", sub, err)
	}
	names, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		return fmt.Errorf("list %s: %w", sub, err)
	}
	for _, n := range names {
		if err := deleteTree(sub + `\` + n); err != nil {
			return err
		}
	}
	if err := registry.DeleteKey(registry.CURRENT_USER, sub); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", sub, err)
	}
	return nil
}

var procSHChangeNotify = windows.NewLazySystemDLL("shell32.dll").NewProc("SHChangeNotify")

const shcneAssocChanged = 0x08000000

func (winRegistry) notify() {
	procSHChangeNotify.Call(shcneAssocChanged, 0, 0, 0)
}

// scheduleSelfDelete starts a hidden command prompt that keeps trying to delete the running
// program, and then home if it is empty, until this process has ended (see cleanupLine).
func scheduleSelfDelete(self, home string) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return
	}
	cmdExe := sys + `\cmd.exe`
	cmd := exec.Command(cmdExe)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), cleanupExeVar+"="+self, cleanupHomeVar+"="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CmdLine:       `"` + cmdExe + `" ` + cleanupLine,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}
