// +build windows

package main

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var lastPercentage = 0.0

type systemCPUTime struct {
	idle   int64
	kernel int64
	user   int64
}

type processCPUTime struct {
	creation int64
	exit     int64
	kernel   int64
	user     int64
}

var prevProcCPU = &processCPUTime{}
var prevSysCPU = &systemCPUTime{}

var (
	modkernel32        = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemTimes = modkernel32.NewProc("GetSystemTimes")
	procGetProcessTimes = modkernel32.NewProc("GetProcessTimes")
	procGetProcessID   = modkernel32.NewProc("GetProcessId")

	modpsapi                 = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

type PROCESS_MEMORY_COUNTERS_EX struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

func getProcessMemoryInfo(h syscall.Handle, mem *PROCESS_MEMORY_COUNTERS_EX) (err error) {
r1, _, e1 := syscall.Syscall(procGetProcessMemoryInfo.Addr(), 3, uintptr(h), uintptr(unsafe.Pointer(mem)), uintptr(unsafe.Sizeof(*mem)))
	if r1 == 0 {
		if e1 != 0 {
			err = error(e1)
		} else {
			err = syscall.EINVAL
		}
	}
	return
}

func getProcessID(h syscall.Handle) (int64, error) {
	var err error
    r1, _, e1 := syscall.Syscall(procGetProcessID.Addr(), 1, uintptr(h), 0,0)
	if r1 == 0 {
		if e1 != 0 {
			err = error(e1)
		} else {
			err = syscall.EINVAL
		}
		return -1, err
	}
	return int64(r1), nil
}

// test to see if the syscall returns the same results...
func getProcessTimes(h syscall.Handle, creationTime, exitTime, kernelTime, userTime *syscall.Filetime) (err error) {
	r1, _, e1 := procGetProcessTimes.Call(uintptr(h), uintptr(unsafe.Pointer(creationTime)), uintptr(unsafe.Pointer(exitTime)), uintptr(unsafe.Pointer(kernelTime)), uintptr(unsafe.Pointer(userTime)))
	if r1 == 0 {
		if e1 != nil {
			err = error(e1)
		} else {
			err = fmt.Errorf("Unable to get process times")
		}
	}
	return
}	

func getSystemTimes(idleTime, kernelTime, userTime *syscall.Filetime) (err error) {
	r1, _, e1 := procGetSystemTimes.Call(uintptr(unsafe.Pointer(idleTime)), uintptr(unsafe.Pointer(kernelTime)), uintptr(unsafe.Pointer(userTime)))
	if r1 == 0 {
		if e1 != nil {
			err = error(e1)
		} else {
			err = fmt.Errorf("Unable to get system times")
		}
	}
	return
}

func fileTimeToInt64(ft *syscall.Filetime) int64 {
	return int64(ft.HighDateTime)<<32 + int64(ft.LowDateTime)
}

func getCPUTimes(sys *systemCPUTime, proc *processCPUTime) error {
	var sIdle, sKernel, sUser, pCreate, pExit, pKernel, pUser syscall.Filetime

	cp, err := syscall.GetCurrentProcess()
	pid, err := getProcessID(cp)
	fmt.Printf("pid = %d", pid)

	if err = syscall.GetProcessTimes(cp, &pCreate, &pExit, &pKernel, &pUser); err != nil {
		return err
	}
	
	//if err = getProcessTimes(cp, &pCreate, &pExit, &pKernel, &pUser); err != nil {
	//	return err
	//}
	
	fmt.Printf("pKernel=%v, pUser=%v\n",
		 pKernel, pUser)	
	 
	if err := getSystemTimes(&sIdle, &sKernel, &sUser); err != nil {
		return err
	}
	
    fmt.Printf("sysIdle=%v, sysKernel=%v, sysUser=%v\n",
		sIdle, sKernel, sUser)

	proc.creation = fileTimeToInt64(&pCreate);
	proc.exit = fileTimeToInt64(&pExit);
	proc.kernel = fileTimeToInt64(&pKernel);
	proc.user = fileTimeToInt64(&pUser);
	
	sys.idle = fileTimeToInt64(&sIdle);
	sys.kernel = fileTimeToInt64(&sKernel);
	sys.user = fileTimeToInt64(&sUser);
	
	return nil
}

func calcPercentageDiff(sysTime, lastSysTime *systemCPUTime, procTime, lastProcTime *processCPUTime) float64 {
	
	sKernelDelta := sysTime.kernel - lastSysTime.kernel
	sUserDelta := sysTime.user - lastSysTime.user
	pKernelDelta := procTime.kernel - lastProcTime.kernel
	pUserDelta := procTime.user - lastProcTime.user

    sysTotal := float64(sKernelDelta + sUserDelta)
	procTotal := float64(pKernelDelta + pUserDelta)
	
    rv := float64((100.0*procTotal)/sysTotal)
	
    fmt.Printf("sKernelDelta=%d\n", sKernelDelta)
    fmt.Printf("sUserDelta=%d\n", sUserDelta)
    fmt.Printf("pKernelDelta=%d\n", pKernelDelta)
    fmt.Printf("pUserDelta=%d\n", pUserDelta)
	fmt.Printf("sysTotal=%f\n", sysTotal)
	fmt.Printf("procTotal=%f\n", procTotal)
	fmt.Printf("rv=%f\n", rv)
	
	return rv
}

var prevCalcTime = int64(0);
func calcPercentageDiff2(lastProcTime, procTime *processCPUTime) float64 {
	
	ft := &syscall.Filetime{}
	
	syscall.GetSystemTimeAsFileTime(ft)
	currentTime := fileTimeToInt64(ft)
	if prevCalcTime == 0 {
		prevCalcTime = currentTime
		return 0.0;
	}
	
	// total previous user and kernel time
	prevUsageTime := float64(lastProcTime.user + lastProcTime.kernel)
	curUsageTime := float64(procTime.user + procTime.kernel)
	
	rv := (curUsageTime - prevUsageTime)/float64((currentTime - prevCalcTime))/100.0
	prevCalcTime = currentTime
	 
	return rv
}

/*
var lastCPU int64
var lastUserCPU int64
//var prevSysCPU int64

func getCPUPercentage2() (float64, error) {
	var fTime syscall.Filetime
	var sIdle, sKernel, sTime, sUser, pCreate, pExit, pKernel, pUser syscall.Filetime
	
	cp, err := syscall.GetCurrentProcess(); if err != nil {
		return -1.0, err
	}
	
	if initialSample {
		// First call is expensive - take a reading, wait,
		// then another to get a baseline.
		syscall.GetSystemTimeAsFileTime(&fTime)
		lastCPU = fTime.Nanoseconds()
	
		syscall.GetProcessTimes(cp, &pCreate, &pExit, &pKernel, &pUser)
		prevSysCPU = pKernel.Nanoseconds();
		lastUserCPU = pKernel.Nanoseconds();
		initialSample = false
	}
	
	syscall.GetSystemTimeAsFileTime(&fTime)
	nowCPU := fTime.Nanoseconds();
	
	syscall.GetProcessTimes(cp, &pCreate, &pExit, &pKernel, &pUser)
	
	return 0, nil
	
}
*/
// maintains a degrading CPU percentage.  An estimate is determined at first reading,
// the subsequent readings will be more accurate.
func getCPUPercentage() (float64, error) {
	
	curSysCPU := &systemCPUTime{}
	curProcCPU := &processCPUTime{}

	if initialSample {
		// First call is expensive - take a reading, wait,
		// then another to get a baseline.
		getCPUTimes(prevSysCPU, prevProcCPU)
		initialSample = false
		return 0.0, nil
	}

	if err := getCPUTimes(curSysCPU, curProcCPU); err != nil {
		return -1, err
	}
		
	rv := calcPercentageDiff(curSysCPU, prevSysCPU, curProcCPU, prevProcCPU)
	fmt.Printf("2nd calc: %f\n", calcPercentageDiff2(prevProcCPU, curProcCPU))
	
	// save previous samples
	prevProcCPU.creation = curProcCPU.creation
	prevProcCPU.exit = curProcCPU.exit
	prevProcCPU.kernel = curProcCPU.kernel
	prevProcCPU.user = curProcCPU.kernel
	
	prevSysCPU.idle = curSysCPU.idle
	prevSysCPU.kernel = curSysCPU.idle
	prevSysCPU.user = curSysCPU.user
	
	return rv, nil
}

// ProcUsage returns processor usage
func ProcUsage(pcpu *float64, rss, vss *int64) error {
	var mem PROCESS_MEMORY_COUNTERS_EX

	currentProcess, err := syscall.GetCurrentProcess()
	if err != nil {
		return err
	}

	getProcessMemoryInfo(currentProcess, &mem)
	fmt.Printf("mem=%v\n", mem)
	fmt.Printf("WorkingSetSize=%d\n",int64(mem.WorkingSetSize))
	//fmt.Printf("PeakWorkingSetSize=%d\n",int64(mem.PeakWorkingSetSize))
    fmt.Printf("PrivateUsage=%d\n",int64(mem.PrivateUsage))
    //fmt.Printf("PagefileUsage=%d\n",int64(mem.PagefileUsage))
	*rss = int64(mem.WorkingSetSize)/1024
	*vss = int64(mem.PrivateUsage)/1024
	
	*pcpu, err = getCPUPercentage()

	return nil
}

func testNoPerfCounters() {

	var pcpu float64
	var rss, vss int64

	for i := 0; i < 100000; i++ {
		err := ProcUsage(&pcpu, &rss, &vss)
		if err != nil {
			fmt.Printf("ProcUsage_PDH() error: %v", err)
			return
		}

		fmt.Printf("ProcUsage info: ")
		fmt.Printf(" rss=%d,", rss)
		fmt.Printf(" vss=%d,", vss)
		fmt.Printf(" pcpu=%f\n", pcpu)
		time.Sleep(250 * time.Millisecond)
	}
}