// +build windows

package main

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	pdh                            = syscall.NewLazyDLL("pdh.dll")
	winPdhOpenQuery                = pdh.NewProc("PdhOpenQuery")
	winPdhCloseQuery               = pdh.NewProc("PdhCloseQuery")
	winPdhAddCounter               = pdh.NewProc("PdhAddCounterW")
	winPdhCollectQueryData         = pdh.NewProc("PdhCollectQueryData")
	winPdhGetFormattedCounterValue = pdh.NewProc("PdhGetFormattedCounterValue")
	winPdhGetFormattedCounterArray = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

// global performance counter query handle and counters
var (
    pcHandle PDH_HQUERY
    pidCounter, cpuCounter, rssCounter, vssCounter PDH_HCOUNTER
    initialSample = true
    prevCPU float64
    prevRss int64
    prevVss int64
    lastSampleTime time.Time	
)

// maximum servers queried (running on this machine simulaneously).
const maxQuerySize = 128

// Keep static memory around to reuse.
var counterResults [maxQuerySize]PDH_FMT_COUNTERVALUE_ITEM_DOUBLE

// PDH Types
type (
	PDH_HQUERY   syscall.Handle // query handle
	PDH_HCOUNTER syscall.Handle // counter handle
)

// PDH constants used here
const (
	PDH_FMT_DOUBLE   = 0x00000200
	PDH_INVALID_DATA = 0xC0000BC6
	PDH_MORE_DATA    = 0x800007D2
)

// PDH_FMT_COUNTERVALUE_DOUBLE - double value 
type PDH_FMT_COUNTERVALUE_DOUBLE struct {
	CStatus     uint32
	DoubleValue float64
}

// PDH_FMT_COUNTERVALUE_ITEM_DOUBLE is an array 
// element of a double value
type PDH_FMT_COUNTERVALUE_ITEM_DOUBLE struct {
	SzName   *uint16 // pointer to a string
	FmtValue PDH_FMT_COUNTERVALUE_DOUBLE
}

func pdhAddCounter(hQuery PDH_HQUERY, szFullCounterPath string, dwUserData uintptr, phCounter *PDH_HCOUNTER) error {
	ptxt, _ := syscall.UTF16PtrFromString(szFullCounterPath)
	r0, _, _ := winPdhAddCounter.Call(
		uintptr(hQuery),
		uintptr(unsafe.Pointer(ptxt)),
		dwUserData,
		uintptr(unsafe.Pointer(phCounter)))

	if r0 != 0 {
		return fmt.Errorf("pdhAddCounter failed. %d", r0)
	}
	return nil
}

func pdhOpenQuery(datasrc *uint16, userdata uint32, query *PDH_HQUERY) error {
	r0, _, _ := syscall.Syscall(winPdhOpenQuery.Addr(), 3, 0 /*uintptr(unsafe.Pointer(datasrc))*/, uintptr(userdata), uintptr(unsafe.Pointer(query)))
	if r0 != 0 {
		return fmt.Errorf("pdhOpenQuery failed. %d", r0)
	}
	return nil
}

/*
func pdhCloseQuery(hQuery PDH_HQUERY) error {
	r0, _, _ := winPdhCloseQuery.Call(uintptr(hQuery))
	if r0 != 0 {
		return fmt.Errorf("pdhCloseQuery failed. %d", r0)
	}
	return nil
}
*/

func pdhCollectQueryData(hQuery PDH_HQUERY) error {
	r0, _, _ := winPdhCollectQueryData.Call(uintptr(hQuery))
	if r0 != 0 {
		return fmt.Errorf("pdhCollectQueryData failed. %d", r0)
	}
	return nil
}

func pdhGetFormattedCounterArrayDouble(hCounter PDH_HCOUNTER, lpdwBufferSize *uint32, lpdwBufferCount *uint32, itemBuffer *PDH_FMT_COUNTERVALUE_ITEM_DOUBLE) uint32 {
	ret, _, _ := winPdhGetFormattedCounterArray.Call(
		uintptr(hCounter),
		uintptr(PDH_FMT_DOUBLE),
		uintptr(unsafe.Pointer(lpdwBufferSize)),
		uintptr(unsafe.Pointer(lpdwBufferCount)),
		uintptr(unsafe.Pointer(itemBuffer)))

	return uint32(ret)
}

// caller must ensure this is threadsafe
func getCounterArrayData(counter PDH_HCOUNTER) ([]float64, error) {
	var bufSize uint32
	var bufCount uint32

	// The API expects an addressable null pointer.
	initialBuf := make([]PDH_FMT_COUNTERVALUE_ITEM_DOUBLE, 1)
	ret := pdhGetFormattedCounterArrayDouble(counter, &bufSize, &bufCount, &initialBuf[0])

	// This function always needs to be called twice...
	if ret == PDH_MORE_DATA {
		ret = pdhGetFormattedCounterArrayDouble(counter, &bufSize, &bufCount, &counterResults[0])
		if ret == 0 {
			rv := make([]float64, bufCount)
			for i := 0; i < int(bufCount); i++ {
				rv[i] = counterResults[i].FmtValue.DoubleValue
			}

			return rv, nil
		}
	}
	// if failure, or too many results, punt
	if ret != 0 {
		return nil, fmt.Errorf("getCounterArrayData: %d", ret)
	}

	return nil, nil
}

// initialize our counters
func initCounters()  (err error) {
		// require an addressible nil pointer
		var source uint16
		if err := pdhOpenQuery(&source, 0, &pcHandle); err != nil {
			return err
		}

		// setup the performance counters, search for all server instances
		name := fmt.Sprintf("%s*", "gnatsd")
		pidQuery := fmt.Sprintf("\\Process(%s)\\ID Process", name)
		cpuQuery := fmt.Sprintf("\\Process(%s)\\%% Processor Time", name)
		rssQuery := fmt.Sprintf("\\Process(%s)\\Working Set - Private", name)
		vssQuery := fmt.Sprintf("\\Process(%s)\\Virtual Bytes", name)

		if err = pdhAddCounter(pcHandle, pidQuery, 0, &pidCounter); err != nil {
			return err
		}
		if err = pdhAddCounter(pcHandle, cpuQuery, 0, &cpuCounter); err != nil {
			return err
		}
		if err = pdhAddCounter(pcHandle, rssQuery, 0, &rssCounter); err != nil {
			return err
		}
		if err = pdhAddCounter(pcHandle, vssQuery, 0, &vssCounter); err != nil {
			return err
		}
		
		// prime the counters by collecting once, and sleep to get somewhat 
		// useful information the first call.  Counters for the CPUs require 
		// collect calls.
		if err = pdhCollectQueryData(pcHandle); err != nil {
		    return err
	    }	
		
		time.Sleep(50)
		
		return nil	
}

func ProcUsagePDH(pcpu *float64, rss, vss *int64) error {
	var err error

	// First time through, initialize counters.
	if initialSample {
		if err = initCounters(); err != nil {
			return err
		}
        initialSample = false
	} else if time.Since(lastSampleTime) < (2 * time.Second) {
		// only refresh every two seconds as to minimize impact 
		// on the server.
		*pcpu = prevCPU
		*rss = prevRss
		*vss = prevVss
		return nil
	}

	// always save the sample time, even on errors.
	defer func() {
		lastSampleTime = time.Now()
		fmt.Printf("Taking sample time.")
	}()

    // refresh the performance counter data
	if err = pdhCollectQueryData(pcHandle); err != nil {
		return err
	}

	// retrieve the fields
	var pidAry, cpuAry, rssAry, vssAry []float64
	if pidAry, err = getCounterArrayData(pidCounter); err != nil {
		return err
	}
	if cpuAry, err = getCounterArrayData(cpuCounter); err != nil {
		return err
	}
	if rssAry, err = getCounterArrayData(rssCounter); err != nil {
		return err
	}
	if vssAry, err = getCounterArrayData(vssCounter); err != nil {
		return err
	}

	// TODO:  Move this, cleanup loop
	pid := syscall.Getpid()
	// TODO:  Remove this...
	pid = 2480
	idx := int(-1)
	for i := range pidAry {
		if int(pidAry[i]) == pid {
			idx = i
			break
		}
	}

	// no pid found...
	if idx < 0 {
		return fmt.Errorf("Could not find pid in performance counter results.")
	}

	// assign values from the performance counters
	*pcpu = cpuAry[idx]
	*rss = int64(rssAry[idx])
	*vss = int64(vssAry[idx])

	// save off cache values
	prevCPU = *pcpu
	prevRss = *rss
	prevVss = *vss

	return nil
}

func main() {

	var pcpu float64
	var rss, vss int64

	for i := 0; i < 100000; i++ {
		err := ProcUsagePDH(&pcpu, &rss, &vss)
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
