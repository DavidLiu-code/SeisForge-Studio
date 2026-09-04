//go:build windows

package main

import (
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// OLE drag/drop is used only to provide DragEnter/DragLeave feedback and to
// accept Explorer drops before WM_DROPFILES fires. DragAcceptFiles remains
// enabled as a fallback for systems where OLE registration is unavailable.
var (
	ole32Drop            = syscall.NewLazyDLL("ole32.dll")
	pOleInitializeDrop   = ole32Drop.NewProc("OleInitialize")
	pRegisterDragDrop    = ole32Drop.NewProc("RegisterDragDrop")
	pRevokeDragDrop      = ole32Drop.NewProc("RevokeDragDrop")
	pReleaseStgMedium    = ole32Drop.NewProc("ReleaseStgMedium")
	oleDropInitialized   bool
	oleSegyTargets       = map[uintptr]*segyDropTarget{}
	segyDropTargetVTable *segyDropTargetVtbl
)

const (
	cfHDrop         = 15
	dvAspectContent = 1
	tymedHGlobal    = 1
	dropEffectNone  = 0
	dropEffectCopy  = 1
	sOK             = 0
	eNoInterface    = 0x80004002
)

type comGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	iidIUnknown    = comGUID{Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIDropTarget = comGUID{Data1: 0x00000122, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

type segyDropTargetVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	DragEnter      uintptr
	DragOver       uintptr
	DragLeave      uintptr
	Drop           uintptr
}

type segyDropTarget struct {
	lpVtbl     *segyDropTargetVtbl
	refCount   int32
	hwnd       uintptr
	targetMode int
	canDrop    bool
}

type formatEtc struct {
	cfFormat uint16
	_pad0    [6]byte
	ptd      uintptr
	dwAspect uint32
	lindex   int32
	tymed    uint32
	_pad1    uint32
}

type stgMedium struct {
	tymed          uint32
	_pad           uint32
	data           uintptr
	pUnkForRelease uintptr
}

type iDataObjectVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetData        uintptr
	GetDataHere    uintptr
	QueryGetData   uintptr
}

func guidEqual(a *comGUID, b *comGUID) bool {
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func dataObjectVtbl(obj uintptr) *iDataObjectVtbl {
	if obj == 0 {
		return nil
	}
	v := *(*uintptr)(unsafe.Pointer(obj))
	if v == 0 {
		return nil
	}
	return (*iDataObjectVtbl)(unsafe.Pointer(v))
}

func segyFormatEtc() formatEtc {
	return formatEtc{cfFormat: cfHDrop, dwAspect: dvAspectContent, lindex: -1, tymed: tymedHGlobal}
}

func dataObjectHasFileDrop(obj uintptr) bool {
	v := dataObjectVtbl(obj)
	if v == nil || v.QueryGetData == 0 {
		return false
	}
	f := segyFormatEtc()
	hr, _, _ := syscall.SyscallN(v.QueryGetData, obj, uintptr(unsafe.Pointer(&f)))
	return int32(hr) >= 0
}

func querySegyPathsFromHDrop(hdrop uintptr) []string {
	if hdrop == 0 {
		return nil
	}
	count, _, _ := pDragQueryFileW.Call(hdrop, 0xffffffff, 0, 0)
	out := make([]string, 0, int(count))
	for i := uintptr(0); i < count; i++ {
		n, _, _ := pDragQueryFileW.Call(hdrop, i, 0, 0)
		if n == 0 {
			continue
		}
		buf := make([]uint16, int(n)+1)
		pDragQueryFileW.Call(hdrop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		path := syscall.UTF16ToString(buf)
		stat, err := os.Stat(path)
		if isSegyPath(path) || (err == nil && stat.IsDir()) {
			out = append(out, path)
		}
	}
	return out
}

func dataObjectSegyPaths(obj uintptr) []string {
	v := dataObjectVtbl(obj)
	if v == nil || v.GetData == 0 {
		return nil
	}
	f := segyFormatEtc()
	var stg stgMedium
	hr, _, _ := syscall.SyscallN(v.GetData, obj, uintptr(unsafe.Pointer(&f)), uintptr(unsafe.Pointer(&stg)))
	if int32(hr) < 0 || stg.tymed != tymedHGlobal || stg.data == 0 {
		return nil
	}
	paths := querySegyPathsFromHDrop(stg.data)
	pReleaseStgMedium.Call(uintptr(unsafe.Pointer(&stg)))
	return paths
}

func setDropEffect(effectPtr uintptr, effect uint32) {
	if effectPtr != 0 {
		*(*uint32)(unsafe.Pointer(effectPtr)) = effect
	}
}

func setStartCenterDragFeedback(active bool) {
	if compareHwnd == 0 || compareAPath != "" {
		return
	}
	if startHomeDragActive == active {
		return
	}
	startHomeDragActive = active
	if active {
		startHomeHover = 3
	} else if startHomeHover == 3 {
		startHomeHover = -1
	}
	invalidateCompareBase()
}

func dropTargetQueryInterface(this, riid, ppv uintptr) uintptr {
	if ppv == 0 || riid == 0 {
		return uintptr(eNoInterface)
	}
	id := (*comGUID)(unsafe.Pointer(riid))
	if guidEqual(id, &iidIUnknown) || guidEqual(id, &iidIDropTarget) {
		*(*uintptr)(unsafe.Pointer(ppv)) = this
		dropTargetAddRef(this)
		return sOK
	}
	*(*uintptr)(unsafe.Pointer(ppv)) = 0
	return uintptr(eNoInterface)
}

func dropTargetAddRef(this uintptr) uintptr {
	if this == 0 {
		return 0
	}
	d := (*segyDropTarget)(unsafe.Pointer(this))
	return uintptr(atomic.AddInt32(&d.refCount, 1))
}

func dropTargetRelease(this uintptr) uintptr {
	if this == 0 {
		return 0
	}
	d := (*segyDropTarget)(unsafe.Pointer(this))
	n := atomic.AddInt32(&d.refCount, -1)
	if n < 0 {
		atomic.StoreInt32(&d.refCount, 0)
		n = 0
	}
	return uintptr(n)
}

func dropTargetDragEnter(this, dataObj, _keyState, _pt, effect uintptr) uintptr {
	d := (*segyDropTarget)(unsafe.Pointer(this))
	d.canDrop = dataObjectHasFileDrop(dataObj)
	if d.canDrop {
		setDropEffect(effect, dropEffectCopy)
		if d.hwnd == compareHwnd {
			setStartCenterDragFeedback(true)
		}
	} else {
		setDropEffect(effect, dropEffectNone)
	}
	return sOK
}

func dropTargetDragOver(this, _keyState, _pt, effect uintptr) uintptr {
	d := (*segyDropTarget)(unsafe.Pointer(this))
	if d.canDrop {
		setDropEffect(effect, dropEffectCopy)
	} else {
		setDropEffect(effect, dropEffectNone)
	}
	return sOK
}

func dropTargetDragLeave(this uintptr) uintptr {
	d := (*segyDropTarget)(unsafe.Pointer(this))
	d.canDrop = false
	if d.hwnd == compareHwnd {
		setStartCenterDragFeedback(false)
	}
	return sOK
}

func dropTargetDrop(this, dataObj, _keyState, _pt, effect uintptr) uintptr {
	d := (*segyDropTarget)(unsafe.Pointer(this))
	if d.hwnd == compareHwnd {
		setStartCenterDragFeedback(false)
	}
	if !d.canDrop {
		setDropEffect(effect, dropEffectNone)
		return sOK
	}
	paths := dataObjectSegyPaths(dataObj)
	d.canDrop = false
	if len(paths) == 0 {
		setDropEffect(effect, dropEffectNone)
		handleWorkspaceDropPaths(nil, d.targetMode)
		return sOK
	}
	setDropEffect(effect, dropEffectCopy)
	handleWorkspaceDropPaths(paths, d.targetMode)
	return sOK
}

func ensureSegyDropTargetVTable() {
	if segyDropTargetVTable != nil {
		return
	}
	segyDropTargetVTable = &segyDropTargetVtbl{
		QueryInterface: syscall.NewCallback(dropTargetQueryInterface),
		AddRef:         syscall.NewCallback(dropTargetAddRef),
		Release:        syscall.NewCallback(dropTargetRelease),
		DragEnter:      syscall.NewCallback(dropTargetDragEnter),
		DragOver:       syscall.NewCallback(dropTargetDragOver),
		DragLeave:      syscall.NewCallback(dropTargetDragLeave),
		Drop:           syscall.NewCallback(dropTargetDrop),
	}
}

func registerOleSegyDropTarget(h uintptr, targetMode int) {
	if h == 0 || oleSegyTargets[h] != nil {
		return
	}
	if !oleDropInitialized {
		hr, _, _ := pOleInitializeDrop.Call(0)
		// S_OK (0) and S_FALSE (1) both mean OLE is initialized for this thread.
		if int32(hr) < 0 {
			return
		}
		oleDropInitialized = true
	}
	ensureSegyDropTargetVTable()
	d := &segyDropTarget{lpVtbl: segyDropTargetVTable, refCount: 1, hwnd: h, targetMode: targetMode}
	hr, _, _ := pRegisterDragDrop.Call(h, uintptr(unsafe.Pointer(d)))
	if int32(hr) < 0 {
		return
	}
	oleSegyTargets[h] = d
	// Avoid receiving the same Explorer drop twice through both OLE and the
	// legacy WM_DROPFILES path. DragAcceptFiles stays enabled only when OLE
	// registration failed, so it remains a transparent fallback.
	pDragAcceptFiles.Call(h, 0)
}

func revokeOleSegyDropTarget(h uintptr) {
	if h == 0 {
		return
	}
	if d := oleSegyTargets[h]; d != nil {
		pRevokeDragDrop.Call(h)
		delete(oleSegyTargets, h)
	}
}
