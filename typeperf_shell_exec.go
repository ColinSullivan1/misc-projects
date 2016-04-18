package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"os"
	"strconv"
	"sync"
)

func main() {

	testProcUsage()
}

func testProcUsage() {
	var pcpu float64
	var rss, vss int64

	if err := procUsage(&pcpu, &rss, &vss); err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("pcpu=%f,rss=%d,vss=%d\n", pcpu, rss, vss)
}

// cache the image name for future calls.
var imageName string
var imageLock sync.Mutex

// Parse the result.  The result will be comma delimited quoted strings,
// containing date time, pid, pcpu, rss, and vss.  All numeric values are
// floating point.
// eg: "04/17/2016 15.38.00.016", "5123.00000", "1.2340000", "123.00000", "123.00000"
func parseResult(line string, pid *int, pcpu *float64, rss, vss *int64) (err error) {
	values := strings.Split(line, ",");
	if len(values) < 4 {
		return errors.New("Invalid result.")
	}
	// values[0] will be date, time, ignore them and parse the pid
	fval, err := strconv.ParseFloat(strings.Trim(values[1],"\""), 64)
	if err != nil {
		return errors.New(fmt.Sprintf("Unable to parse pid: %s", values[1]))
	}
	*pid = int(fval)

	// parse pcpu
	*pcpu, err = strconv.ParseFloat(strings.Trim(values[2],"\""), 64)
	if err != nil {
		return errors.New(fmt.Sprintf("Unable to parse percent cpu: %s", values[2]))
	}

	// parse private bytes (rss)
	fval, err = strconv.ParseFloat(strings.Trim(values[3],"\""), 64)
	if err != nil {
		return errors.New(fmt.Sprintf("Unable to parse private bytes: %s", values[3]))
	}
	*rss = int64(fval)

	// parse virtual bytes (vsz)
	fval, err  = strconv.ParseFloat(strings.Trim(values[4],"\""), 64)
	if err != nil {
		return errors.New(fmt.Sprintf("Unable to parse virtual bytes: %s", values[4]))
	}
	*vss = int64(fval)

	return nil
}

// getStatsForProcess retrieves information for a given instance name.
// The native command line utility to get pcpu, rss, and vsz equivalents
// is the typeperf facility, which queries performance counter values.
// Notably, typeperf cannot search using a pid, but instead uses a volatile
// process image name.  If there is more than one instance, #<instancecount> is
// appended to the image name. An alternative is to map the Pdh* native windows
// API from kernel32.dll, etc. and call those APIs directly, but this is the
// simplest approach.
func getStatsForProcess(instName string, pcpu *float64, rss, vss *int64, pid *int) (err error) {

	// setup the performance counters to query by our instance name
	pidCounter :=  fmt.Sprintf("\\Process(%s)\\ID Process", instName)
	pcpuCounter := fmt.Sprintf("\\Process(%s)\\%% Processor Time", instName)
	rssCounter :=  fmt.Sprintf("\\Process(%s)\\Private Bytes", instName)
	vssCounter :=  fmt.Sprintf("\\Process(%s)\\Virtual Bytes", instName)

	// query the counters using typeper. "-sc","1" indicates to return one
	// set of data (rather than continuous monitoring)
	out, err := exec.Command("typeperf", pidCounter, pcpuCounter,
		rssCounter, vssCounter,
		"-sc", "1").Output()
	if err != nil {
		// Signal that the command ran, but the image instance was not found
		// through a PID of -1.
		if strings.Contains(string(out), "The data is not valid") {
			fmt.Printf("DEBUG: failed (data not valid):  %s\n", string(out))
			*pid = -1
			return nil;
		} else {
			// something wrong issuing the command
			// TODO = debug trace out
			fmt.Printf("DEBUG: failed:  %s\n", string(out))
			return errors.New(fmt.Sprintf("typeperf failed: %v", err))
		}
	}

	results := strings.Split(string(out),"\r\n")
	//results[0] = newline
	//results[1] = headers
	//results[2] = values
	//ignore the rest...
	if len(results) < 3 {
		return errors.New(fmt.Sprintf("invalid result"))
	}

	err = parseResult(results[2], pid, pcpu, rss, vss)
	if err != nil {
		return err
	}

	return nil
}

func procUsage(pcpu *float64, rss, vss *int64) error {
	var ppid int = -1

	imageLock.Lock();
	name := imageName
	imageLock.Unlock();

	// Get the pid to discover the image name of this process.
	procPid := os.Getpid()

	// if we have cached the image name try that first
	if name != "" {
		err := getStatsForProcess(name, pcpu, rss, vss, &ppid)
		if err != nil {
			return err
		}
		// If the instance name's pid matches ours, we're done.
		// Otherwise, this instance has been renamed, which is possible
		// as other gnatsd instances start and stop on the system.
		if ppid == procPid {
			return nil
		}
	}
	// If we get here, the instance name is invalid (nil, or out of sync)
	// Find the correct image name and cache it.
	for i:= 0; ppid != procPid; i++{
		name = fmt.Sprintf("gnatsd#%d", i)
		err := getStatsForProcess(name, pcpu, rss, vss, &ppid)
		if err != nil {
			return err
		}

		// Bail out if an image name is not found.
		if ppid < 0 {
			return errors.New("unable to find process image")
		}
		// if the pids equal, this is the right process and cache our
		// image name
		if ppid == procPid {
			imageLock.Lock()
			imageName = name
			imageLock.Unlock()
			break;
		}
	}
	if ppid == -1 {
		return errors.New("unable to find process counters")
	}
	return nil
}

