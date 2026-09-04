//go:build windows

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	lic "github.com/DavidLiu-code/SeisForge-Studio/internal/license"
)

var (
	advapi32               = syscall.NewLazyDLL("advapi32.dll")
	pRegGetValueW          = advapi32.NewProc("RegGetValueW")
	pGetVolumeInformationW = kernel32.NewProc("GetVolumeInformationW")

	licenseHwnd        uintptr
	licenseMachineEdit uintptr
	licenseCodeEdit    uintptr
	licenseStatus      uintptr
	licenseActivated   bool
	licenseCancelled   bool
	currentLicense     lic.Info
	currentMachine     [10]byte
)

const (
	HKEY_LOCAL_MACHINE    = 0x80000002
	RRF_RT_REG_SZ         = 0x00000002
	RRF_SUBKEY_WOW6464KEY = 0x00010000
	IDLIC_ACTIVATE        = 7101
	IDLIC_EXIT            = 7102
)

func readMachineGuid() (string, error) {
	sub := u16(`SOFTWARE\Microsoft\Cryptography`)
	val := u16("MachineGuid")
	buf := make([]uint16, 256)
	cb := uint32(len(buf) * 2)
	r, _, _ := pRegGetValueW.Call(
		uintptr(HKEY_LOCAL_MACHINE),
		uintptr(unsafe.Pointer(sub)),
		uintptr(unsafe.Pointer(val)),
		uintptr(RRF_RT_REG_SZ|RRF_SUBKEY_WOW6464KEY),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&cb)),
	)
	if r != 0 {
		return "", fmt.Errorf("cannot read Windows MachineGuid (error %d)", r)
	}
	s := strings.TrimSpace(syscall.UTF16ToString(buf))
	if s == "" {
		return "", fmt.Errorf("Windows MachineGuid is empty")
	}
	return s, nil
}

func systemVolumeSerial() uint32 {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	root := u16(drive + `\`)
	var serial uint32
	r, _, _ := pGetVolumeInformationW.Call(
		uintptr(unsafe.Pointer(root)), 0, 0,
		uintptr(unsafe.Pointer(&serial)), 0, 0, 0, 0,
	)
	if r == 0 {
		return 0
	}
	return serial
}

func machineFingerprint() ([10]byte, string, error) {
	var out [10]byte
	guid, err := readMachineGuid()
	if err != nil {
		return out, "", err
	}
	serial := systemVolumeSerial()
	raw := fmt.Sprintf("Limage-Machine-v1|%s|%08X", strings.ToUpper(guid), serial)
	sum := sha256.Sum256([]byte(raw))
	copy(out[:], sum[:10])
	return out, lic.EncodeMachineID(out), nil
}

func licenseDirectory() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		if d, err := os.UserConfigDir(); err == nil {
			base = d
		}
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "Limage")
}
func licensePath() string { return filepath.Join(licenseDirectory(), "license.lic") }
func clockPath() string   { return filepath.Join(licenseDirectory(), "clock.state") }

func clockMAC(machine [10]byte, unix int64) string {
	k := sha256.Sum256(append(append([]byte{}, machine[:]...), []byte("|Limage-clock-state-v1")...))
	h := hmac.New(sha256.New, k[:])
	fmt.Fprintf(h, "%d", unix)
	return hex.EncodeToString(h.Sum(nil))
}

func checkClockRollback(machine [10]byte, now time.Time) error {
	b, err := os.ReadFile(clockPath())
	if err != nil {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(string(b)), ":")
	if len(parts) != 2 {
		return nil
	}
	last, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil
	}
	if !hmac.Equal([]byte(strings.ToLower(parts[1])), []byte(clockMAC(machine, last))) {
		return nil
	}
	// Six hours of tolerance avoids false positives from timezone/DST/manual
	// corrections while still catching meaningful clock rollback.
	if now.Unix()+6*3600 < last {
		return fmt.Errorf("system clock appears to have been moved backwards")
	}
	return nil
}

func updateClockState(machine [10]byte, now time.Time) {
	_ = os.MkdirAll(licenseDirectory(), 0700)
	last := now.Unix()
	if b, err := os.ReadFile(clockPath()); err == nil {
		parts := strings.Split(strings.TrimSpace(string(b)), ":")
		if len(parts) == 2 {
			if old, e := strconv.ParseInt(parts[0], 10, 64); e == nil && old > last && hmac.Equal([]byte(strings.ToLower(parts[1])), []byte(clockMAC(machine, old))) {
				last = old
			}
		}
	}
	text := fmt.Sprintf("%d:%s\n", last, clockMAC(machine, last))
	_ = os.WriteFile(clockPath(), []byte(text), 0600)
}

func loadInstalledLicense(machine [10]byte) (lic.Info, error) {
	b, err := os.ReadFile(licensePath())
	if err != nil {
		return lic.Info{}, err
	}
	now := time.Now()
	if err := checkClockRollback(machine, now); err != nil {
		return lic.Info{}, err
	}
	info, err := lic.Verify(string(b), machine, now)
	if err != nil {
		return lic.Info{}, err
	}
	updateClockState(machine, now)
	return info, nil
}

func saveInstalledLicense(code string, machine [10]byte) error {
	if err := os.MkdirAll(licenseDirectory(), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(licensePath(), []byte(strings.TrimSpace(code)+"\n"), 0600); err != nil {
		return err
	}
	updateClockState(machine, time.Now())
	return nil
}

func licenseSummary() string {
	if currentLicense.Licensee == "" {
		return "Unlicensed"
	}
	return fmt.Sprintf("Licensed to %s | Expires %s", currentLicense.Licensee, currentLicense.Expires.Format("2006-01-02"))
}

func createLicenseUI(machineCode string, reason string) {
	createCtrl(licenseHwnd, "STATIC", APP_NAME+" 软件激活", WS_CHILD|WS_VISIBLE, 18, 14, 320, 22, 0)
	createCtrl(licenseHwnd, "STATIC", "本机机器码（发给软件作者生成注册码）：", WS_CHILD|WS_VISIBLE, 18, 48, 300, 18, 0)
	licenseMachineEdit = createCtrl(licenseHwnd, "EDIT", machineCode, WS_CHILD|WS_VISIBLE|WS_BORDER|ES_READONLY|ES_AUTOHSCROLL, 18, 70, 430, 24, 0)
	createCtrl(licenseHwnd, "STATIC", "注册码（默认有效期 6 个月）：", WS_CHILD|WS_VISIBLE, 18, 108, 250, 18, 0)
	licenseCodeEdit = createCtrl(licenseHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|WS_TABSTOP|0x0004|0x0040, 18, 130, 430, 92, 0) // ES_MULTILINE|ES_AUTOVSCROLL
	createCtrl(licenseHwnd, "BUTTON", "激活", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON, 110, 238, 90, 28, IDLIC_ACTIVATE)
	createCtrl(licenseHwnd, "BUTTON", "退出", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 260, 238, 90, 28, IDLIC_EXIT)
	licenseStatus = createCtrl(licenseHwnd, "STATIC", reason, WS_CHILD|WS_VISIBLE, 18, 280, 430, 40, 0)
}

func licenseWndProc(h uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		id := int(wp & 0xffff)
		switch id {
		case IDLIC_ACTIVATE:
			code := strings.TrimSpace(getText(licenseCodeEdit))
			info, err := lic.Verify(code, currentMachine, time.Now())
			if err != nil {
				setText(licenseStatus, "激活失败："+err.Error())
				return 0
			}
			if err := saveInstalledLicense(code, currentMachine); err != nil {
				setText(licenseStatus, "无法保存许可证："+err.Error())
				return 0
			}
			currentLicense = info
			licenseActivated = true
			pDestroyWindow.Call(h)
			return 0
		case IDLIC_EXIT:
			licenseCancelled = true
			pDestroyWindow.Call(h)
			return 0
		}
	case WM_CLOSE:
		licenseCancelled = true
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		licenseHwnd = 0
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wp, lp)
	return r
}

func ensureActivated(hinst uintptr) bool {
	machine, machineCode, err := machineFingerprint()
	if err != nil {
		message(0, APP_NAME+" 激活", "无法生成机器码："+err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	currentMachine = machine
	if info, err := loadInstalledLicense(machine); err == nil {
		currentLicense = info
		return true
	}
	reason := "尚未激活。请把机器码发送给软件作者获取注册码。"
	if _, err := os.Stat(licensePath()); err == nil {
		if _, e := loadInstalledLicense(machine); e != nil {
			reason = "当前许可证无效：" + e.Error()
		}
	}
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("Limage64License"))), uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" - 软件激活"))), WS_OVERLAPPEDWINDOW,
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 485, 375, 0, 0, hinst, 0)
	if h == 0 {
		return false
	}
	licenseHwnd = h
	createLicenseUI(machineCode, reason)
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
	var m MSG
	for !licenseActivated && !licenseCancelled && licenseHwnd != 0 {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			licenseCancelled = true
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return licenseActivated
}
